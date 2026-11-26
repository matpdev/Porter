package main

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"codeberg.org/matheus/porter/internal/agents"
	"codeberg.org/matheus/porter/internal/odradek"
	"codeberg.org/matheus/porter/internal/odradek/curator"
	"codeberg.org/matheus/porter/internal/odradek/renderer"
	"codeberg.org/matheus/porter/internal/pipeline"
	"codeberg.org/matheus/porter/internal/porter/linkedin"
	"codeberg.org/matheus/porter/internal/porter/sidequest"
	"codeberg.org/matheus/porter/internal/porter/telegram"
	"codeberg.org/matheus/porter/internal/settings"
	"codeberg.org/matheus/porter/internal/store"
	"github.com/spf13/cobra"
)

func main() {
	var dbPath string
	var mediaDir string

	rootCmd := &cobra.Command{
		Use:   "porter",
		Short: "Porter & Odradek — autonomous tech publisher for LinkedIn and sidequestlab.dev",
	}
	rootCmd.PersistentFlags().StringVar(&dbPath, "db", "./data/app.db", "Path to SQLite database")
	rootCmd.PersistentFlags().StringVar(&mediaDir, "media-dir", "./data/media", "Directory for rendered PNG media")

	rootCmd.AddCommand(
		daemonCmd(&dbPath, &mediaDir),
		runOnceCmd(&dbPath, &mediaDir),
		configCmd(&dbPath),
		themeCmd(&dbPath),
		patternCmd(&dbPath),
		authCmd(&dbPath),
	)

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func openServices(ctx context.Context, dbPath string) (*sql.DB, *store.Repository, *settings.Manager, error) {
	db, err := store.Open(ctx, dbPath)
	if err != nil {
		return nil, nil, nil, err
	}
	repo := store.NewRepository(db)
	mgr := settings.NewManager(repo)
	if err := mgr.SeedDefaults(ctx); err != nil {
		_ = db.Close()
		return nil, nil, nil, err
	}
	return db, repo, mgr, nil
}

func daemonCmd(dbPath, mediaDir *string) *cobra.Command {
	return &cobra.Command{
		Use:   "daemon",
		Short: "Run the Porter + Odradek daemon (Telegram bot, scheduler, reminders)",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()

			logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
			db, repo, mgr, err := openServices(ctx, *dbPath)
			if err != nil {
				return err
			}
			defer db.Close()

			if err := os.MkdirAll(*mediaDir, 0o755); err != nil {
				return fmt.Errorf("create media dir: %w", err)
			}

			oauthSvc := linkedin.NewOAuthService(repo, mgr, nil)
			botSvc := telegram.NewBotService(repo, mgr, oauthSvc, logger)
			tokenMon := linkedin.NewTokenMonitor(repo)

			rend := renderer.NewRenderer(*mediaDir)
			defer rend.Close()

			curSvc := curator.NewService(repo, mgr, nil, nil, nil, nil)
			writer := agents.NewWriterAgent(mgr)
			qa := odradek.NewQAAgent(mgr)
			liClient := linkedin.NewClient(repo, mgr, nil)
			sqPub := sidequest.NewPublisher(mgr)

			orch := pipeline.NewOrchestrator(repo, mgr, curSvc, writer, rend, qa, liClient, sqPub, botSvc, logger)
			sched := pipeline.NewScheduler(mgr, orch, logger)

			botSvc.SetIntakeHandler(orch)
			botSvc.SetOnSettingChanged(func(key string) {
				if key == settings.KeyCronSchedule || key == settings.KeyReviewReminderSchedule {
					_ = sched.Reload(ctx)
				}
			})

			if err := sched.Start(ctx); err != nil {
				return fmt.Errorf("start scheduler: %w", err)
			}

			go runBotLoop(ctx, mgr, botSvc, logger)
			go runTokenMonitorLoop(ctx, tokenMon, botSvc, logger)

			logger.Info("porter & odradek initialized", "db", *dbPath, "media_dir", *mediaDir)
			<-ctx.Done()
			logger.Info("shutting down gracefully")
			sched.Stop()
			return nil
		},
	}
}

func runOnceCmd(dbPath, mediaDir *string) *cobra.Command {
	return &cobra.Command{
		Use:   "run-once",
		Short: "Run a single scheduled curation + drafting + QA cycle from the CLI",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()

			logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
			db, repo, mgr, err := openServices(ctx, *dbPath)
			if err != nil {
				return err
			}
			defer db.Close()

			rend := renderer.NewRenderer(*mediaDir)
			defer rend.Close()

			curSvc := curator.NewService(repo, mgr, nil, nil, nil, nil)
			writer := agents.NewWriterAgent(mgr)
			qa := odradek.NewQAAgent(mgr)
			liClient := linkedin.NewClient(repo, mgr, nil)
			sqPub := sidequest.NewPublisher(mgr)

			orch := pipeline.NewOrchestrator(repo, mgr, curSvc, writer, rend, qa, liClient, sqPub, nil, logger)
			return orch.RunScheduled(ctx)
		},
	}
}

func configCmd(dbPath *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Manage live runtime settings stored in SQLite (no restart needed)",
	}

	cmd.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List all settings (secrets masked)",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			db, _, mgr, err := openServices(ctx, *dbPath)
			if err != nil {
				return err
			}
			defer db.Close()

			list, err := mgr.ListMasked(ctx)
			if err != nil {
				return err
			}
			for _, s := range list {
				fmt.Printf("%-34s = %s\n", s.Key, s.Value)
			}
			return nil
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "get <key>",
		Short: "Get a setting value",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			db, _, mgr, err := openServices(ctx, *dbPath)
			if err != nil {
				return err
			}
			defer db.Close()

			val, err := mgr.GetString(ctx, args[0])
			if err != nil {
				return err
			}
			fmt.Println(val)
			return nil
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "set <key> <value>",
		Short: "Set a setting value immediately without restarting the daemon",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			db, _, mgr, err := openServices(ctx, *dbPath)
			if err != nil {
				return err
			}
			defer db.Close()

			if err := mgr.Set(ctx, args[0], args[1]); err != nil {
				return err
			}
			fmt.Printf("updated %s\n", args[0])
			return nil
		},
	})

	return cmd
}

func themeCmd(dbPath *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "theme",
		Short: "Plan and inspect weekly themes for LinkedIn and sidequestlab.dev",
	}

	var title, angle, sourceURL, weekStart, day, pattern string
	var targetLinkedIn, targetSidequest bool

	addCmd := &cobra.Command{
		Use:   "add",
		Short: "Add a planned theme to the weekly queue",
		RunE: func(cmd *cobra.Command, args []string) error {
			if weekStart == "" {
				weekStart = currentMonday()
			}
			ctx := context.Background()
			db, repo, _, err := openServices(ctx, *dbPath)
			if err != nil {
				return err
			}
			defer db.Close()

			var patternPtr *string
			if pattern != "" {
				if _, err := repo.GetOrCreatePattern(ctx, pattern, pattern); err != nil {
					return err
				}
				patternPtr = &pattern
			}

			id, err := repo.CreateWeeklyTheme(ctx, store.WeeklyTheme{
				WeekStart:       weekStart,
				ScheduledDay:    day,
				PatternSlug:     patternPtr,
				Title:           title,
				Angle:           angle,
				SourceURL:       sourceURL,
				TargetLinkedIn:  targetLinkedIn,
				TargetSidequest: targetSidequest,
			})
			if err != nil {
				return err
			}
			fmt.Printf("planned theme #%d for week %s (linkedin=%v, sidequest=%v)\n", id, weekStart, targetLinkedIn, targetSidequest)
			return nil
		},
	}
	addCmd.Flags().StringVar(&title, "title", "", "Theme title (required)")
	addCmd.Flags().StringVar(&angle, "angle", "", "Technical angle or notes for the writer")
	addCmd.Flags().StringVar(&sourceURL, "url", "", "Optional reference URL")
	addCmd.Flags().StringVar(&weekStart, "week", "", "Monday YYYY-MM-DD (defaults to current week)")
	addCmd.Flags().StringVar(&day, "day", "", "Optional day label (e.g. tuesday)")
	addCmd.Flags().StringVar(&pattern, "pattern", "", "Optional Odradek topic pattern slug")
	addCmd.Flags().BoolVar(&targetLinkedIn, "linkedin", true, "Publish to LinkedIn")
	addCmd.Flags().BoolVar(&targetSidequest, "sidequest", false, "Also publish MDX article to sidequestlab.dev")
	_ = addCmd.MarkFlagRequired("title")

	var filterWeek string
	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List weekly themes",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			db, repo, _, err := openServices(ctx, *dbPath)
			if err != nil {
				return err
			}
			defer db.Close()

			themes, err := repo.ListWeeklyThemes(ctx, filterWeek)
			if err != nil {
				return err
			}
			for _, t := range themes {
				fmt.Printf("#%-3d [%s] week=%s li=%v sq=%v | %s\n",
					t.ID, t.Status, t.WeekStart, t.TargetLinkedIn, t.TargetSidequest, t.Title)
			}
			return nil
		},
	}
	listCmd.Flags().StringVar(&filterWeek, "week", "", "Filter by week YYYY-MM-DD")

	cmd.AddCommand(addCmd, listCmd)
	return cmd
}

func patternCmd(dbPath *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pattern",
		Short: "Inspect and manage Odradek trusted topic patterns",
	}

	cmd.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List known topic patterns and trust status",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			db, repo, _, err := openServices(ctx, *dbPath)
			if err != nil {
				return err
			}
			defer db.Close()

			patterns, err := repo.ListPatterns(ctx)
			if err != nil {
				return err
			}
			for _, p := range patterns {
				fmt.Printf("%-24s auto=%v approvals=%d guidelines=%q\n",
					p.Slug, p.AutoPublish, p.ApprovedCount, p.Guidelines)
			}
			return nil
		},
	})

	var guidelines string
	var autoPublish bool
	trustCmd := &cobra.Command{
		Use:   "trust <slug>",
		Short: "Set auto-publish trust and guidelines for a topic pattern",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			db, repo, _, err := openServices(ctx, *dbPath)
			if err != nil {
				return err
			}
			defer db.Close()

			if _, err := repo.GetOrCreatePattern(ctx, args[0], args[0]); err != nil {
				return err
			}
			if err := repo.SetPatternTrust(ctx, args[0], autoPublish, guidelines); err != nil {
				return err
			}
			fmt.Printf("pattern %s updated (auto=%v)\n", args[0], autoPublish)
			return nil
		},
	}
	trustCmd.Flags().BoolVar(&autoPublish, "auto", true, "Allow auto-publishing when QA has no uncertainty")
	trustCmd.Flags().StringVar(&guidelines, "guidelines", "", "Writer guidelines for this topic")

	cmd.AddCommand(trustCmd)
	return cmd
}

func authCmd(dbPath *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "auth",
		Short: "Authenticate external providers",
	}

	cmd.AddCommand(&cobra.Command{
		Use:   "linkedin",
		Short: "Run LinkedIn OAuth2 authorization flow",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()

			db, repo, mgr, err := openServices(ctx, *dbPath)
			if err != nil {
				return err
			}
			defer db.Close()

			oauthSvc := linkedin.NewOAuthService(repo, mgr, nil)
			authURL, state, err := oauthSvc.BuildAuthURL(ctx)
			if err != nil {
				return err
			}
			fmt.Printf("Open this URL in your browser to authorize LinkedIn:\n\n%s\n\nWaiting for callback...\n", authURL)

			tok, err := oauthSvc.RunCallbackServer(ctx, state, 5*time.Minute)
			if err != nil {
				return err
			}
			fmt.Printf("LinkedIn connected! person_urn=%s expires_at=%s\n", tok.PersonURN, tok.ExpiresAt.Format(time.RFC3339))
			return nil
		},
	})

	return cmd
}

func runBotLoop(ctx context.Context, mgr *settings.Manager, botSvc *telegram.BotService, logger *slog.Logger) {
	warned := false
	for {
		if ctx.Err() != nil {
			return
		}
		token, err := mgr.GetString(ctx, settings.KeyTelegramBotToken)
		if err == nil && strings.TrimSpace(token) != "" {
			if err := botSvc.Start(ctx, strings.TrimSpace(token)); err != nil && ctx.Err() == nil {
				logger.Error("telegram bot stopped", "err", err)
			}
			return
		}
		if !warned {
			logger.Warn("telegram.bot_token is unset; waiting for `porter config set telegram.bot_token <TOKEN>`")
			warned = true
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
		}
	}
}

func runTokenMonitorLoop(ctx context.Context, mon *linkedin.TokenMonitor, botSvc *telegram.BotService, logger *slog.Logger) {
	check := func() {
		if err := mon.CheckAndNotify(ctx, botSvc.SendAlert); err != nil {
			logger.Debug("token monitor check skipped/failed", "err", err)
		}
	}
	ticker := time.NewTicker(6 * time.Hour)
	defer ticker.Stop()

	// Initial check shortly after startup so bot has time to connect.
	select {
	case <-ctx.Done():
		return
	case <-time.After(10 * time.Second):
		check()
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			check()
		}
	}
}

func currentMonday() string {
	now := time.Now()
	offset := (int(now.Weekday()) + 6) % 7
	monday := now.AddDate(0, 0, -offset)
	return monday.Format("2006-01-02")
}
