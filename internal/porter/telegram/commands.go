package telegram

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"codeberg.org/matheus/porter/internal/porter/linkedin"
	"codeberg.org/matheus/porter/internal/settings"
	"codeberg.org/matheus/porter/internal/store"
	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

func (s *BotService) handleStart(ctx context.Context, b *bot.Bot, update *models.Update) {
	if update.Message == nil || update.Message.From == nil {
		return
	}
	ok, configured, err := s.IsAuthorized(ctx, update.Message.From.ID)
	if err != nil {
		return
	}
	if !ok {
		if !configured {
			id := update.Message.From.ID
			_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
				ChatID: update.Message.Chat.ID,
				Text: fmt.Sprintf(
					"👋 Porter online.\nYour Telegram Admin Chat ID is: %d\n\nConfigure it via CLI (no restart needed):\nporter config set telegram.admin_chat_id %d",
					id, id,
				),
			})
		}
		return
	}

	liStatus := "Not connected (run /auth)"
	if tok, err := s.repo.GetOAuthToken(ctx, linkedin.ProviderLinkedIn); err == nil {
		days := int(time.Until(tok.ExpiresAt).Hours() / 24)
		liStatus = fmt.Sprintf("Connected (%dd left)", days)
	}
	pending, _ := s.repo.ListPendingApprovalPosts(ctx)
	themes, _ := s.repo.ListWeeklyThemes(ctx, currentMonday())

	_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID: update.Message.Chat.ID,
		Text: fmt.Sprintf(
			"🎒 Porter & Odradek ready.\n• LinkedIn: %s\n• Weekly themes: %d\n• Awaiting review: %d\n\nUse /help to see commands or send text, a link, or a voice note.",
			liStatus, len(themes), len(pending),
		),
	})
}

func (s *BotService) handleHelp(ctx context.Context, b *bot.Bot, update *models.Update) {
	s.withAdmin(func(ctx context.Context, b *bot.Bot, update *models.Update) {
		_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: update.Message.Chat.ID,
			Text: strings.Join([]string{
				"Commands:",
				"/config — view settings & quick toggles",
				"/set <key> <value> — update setting live",
				"/tone — choose writer tone (technical/informational/informal/formal)",
				"/week — list weekly planned themes",
				"/add_theme <title> — add theme to current week",
				"/patterns — list Odradek topic patterns",
				"/trust <slug> [guidelines] — trust pattern for auto-publish",
				"/run — run scheduled curation pipeline now",
				"/devlog — draft weekly update from GitHub & Codeberg",
				"/insights — view QA quality & LinkedIn growth insights",
				"/auth — connect/renew LinkedIn OAuth2",
			}, "\n"),
		})
	})(ctx, b, update)
}

func (s *BotService) handleConfig(ctx context.Context, b *bot.Bot, update *models.Update) {
	text, kb, err := s.buildConfigView(ctx)
	if err != nil {
		s.reply(ctx, b, update, "Error: "+err.Error())
		return
	}
	_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:      update.Message.Chat.ID,
		Text:        text,
		ReplyMarkup: kb,
	})
}

func (s *BotService) buildConfigView(ctx context.Context) (string, *models.InlineKeyboardMarkup, error) {
	list, err := s.settings.ListMasked(ctx)
	if err != nil {
		return "", nil, err
	}
	var sb strings.Builder
	sb.WriteString("⚙️ Runtime Settings (live, no restart):\n\n")
	for _, item := range list {
		sb.WriteString(fmt.Sprintf("%s = %s\n", item.Key, item.Value))
	}

	dryRun, _ := s.settings.GetBool(ctx, settings.KeyDryRun)
	sqEnabled, _ := s.settings.GetBool(ctx, settings.KeySidequestEnabled)
	tgApprove, _ := s.settings.GetBool(ctx, settings.KeyTelegramRequireApproval)

	kb := &models.InlineKeyboardMarkup{
		InlineKeyboard: [][]models.InlineKeyboardButton{
			{
				{Text: toggleLabel("dry_run", dryRun), CallbackData: "toggle:" + settings.KeyDryRun},
				{Text: toggleLabel("sidequest", sqEnabled), CallbackData: "toggle:" + settings.KeySidequestEnabled},
			},
			{
				{Text: toggleLabel("tg_approval", tgApprove), CallbackData: "toggle:" + settings.KeyTelegramRequireApproval},
			},
		},
	}
	return sb.String(), kb, nil
}

func (s *BotService) handleSet(ctx context.Context, b *bot.Bot, update *models.Update) {
	key, val, ok := parseKeyValueCommand(update.Message.Text, "/set")
	if !ok {
		s.reply(ctx, b, update, "Usage: /set <key> <value>")
		return
	}
	if err := s.settings.Set(ctx, key, val); err != nil {
		s.reply(ctx, b, update, "Error: "+err.Error())
		return
	}
	if s.onSettingChanged != nil {
		s.onSettingChanged(key)
	}
	s.reply(ctx, b, update, fmt.Sprintf("✅ Updated %s (active immediately).", key))
}

func (s *BotService) handleTone(ctx context.Context, b *bot.Bot, update *models.Update) {
	text, kb := s.buildToneView(ctx)
	_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:      update.Message.Chat.ID,
		Text:        text,
		ReplyMarkup: kb,
	})
}

func (s *BotService) buildToneView(ctx context.Context) (string, *models.InlineKeyboardMarkup) {
	cur, _ := s.settings.GetString(ctx, settings.KeyWriterTone)
	if cur == "" {
		cur = "technical"
	}
	btn := func(val, label string) models.InlineKeyboardButton {
		prefix := ""
		if strings.EqualFold(cur, val) {
			prefix = "✅ "
		}
		return models.InlineKeyboardButton{Text: prefix + label, CallbackData: "tone:" + val}
	}
	kb := &models.InlineKeyboardMarkup{
		InlineKeyboard: [][]models.InlineKeyboardButton{
			{btn("technical", "Technical"), btn("informational", "Informational")},
			{btn("informal", "Informal"), btn("formal", "Formal")},
		},
	}
	return fmt.Sprintf("🎙 Current Writer Tone: %s\nSelect a tone (applies immediately):", cur), kb
}

func (s *BotService) handleWeek(ctx context.Context, b *bot.Bot, update *models.Update) {
	week := currentMonday()
	themes, err := s.repo.ListWeeklyThemes(ctx, week)
	if err != nil {
		s.reply(ctx, b, update, "Error: "+err.Error())
		return
	}
	if len(themes) == 0 {
		s.reply(ctx, b, update, fmt.Sprintf("📅 No themes planned for week %s.\nAdd one with: /add_theme <title>", week))
		return
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("📅 Weekly Themes (%s):\n\n", week))
	for _, t := range themes {
		targets := "LI"
		if t.TargetSidequest {
			targets += "+Sidequest"
		}
		sb.WriteString(fmt.Sprintf("#%d [%s] (%s) %s\n", t.ID, t.Status, targets, t.Title))
	}
	s.reply(ctx, b, update, sb.String())
}

func (s *BotService) handleAddTheme(ctx context.Context, b *bot.Bot, update *models.Update) {
	title := strings.TrimSpace(strings.TrimPrefix(update.Message.Text, "/add_theme"))
	if title == "" {
		s.reply(ctx, b, update, "Usage: /add_theme <title> (optional: prefix with [sidequest] to also target sidequestlab.dev)")
		return
	}
	targetSidequest := false
	if strings.HasPrefix(strings.ToLower(title), "[sidequest]") {
		targetSidequest = true
		title = strings.TrimSpace(title[len("[sidequest]"):])
	}
	id, err := s.repo.CreateWeeklyTheme(ctx, store.WeeklyTheme{
		WeekStart:       currentMonday(),
		Title:           title,
		TargetLinkedIn:  true,
		TargetSidequest: targetSidequest,
	})
	if err != nil {
		s.reply(ctx, b, update, "Error: "+err.Error())
		return
	}
	s.reply(ctx, b, update, fmt.Sprintf("📝 Added weekly theme #%d: %s (sidequest=%v)", id, title, targetSidequest))
}

func (s *BotService) handlePatterns(ctx context.Context, b *bot.Bot, update *models.Update) {
	patterns, err := s.repo.ListPatterns(ctx)
	if err != nil {
		s.reply(ctx, b, update, "Error: "+err.Error())
		return
	}
	if len(patterns) == 0 {
		s.reply(ctx, b, update, "No topic patterns recorded yet.")
		return
	}
	var sb strings.Builder
	sb.WriteString("🧭 Odradek Topic Patterns:\n\n")
	for _, p := range patterns {
		trust := "review"
		if p.AutoPublish {
			trust = "auto"
		}
		sb.WriteString(fmt.Sprintf("• %s [%s, %d approvals] %s\n", p.Slug, trust, p.ApprovedCount, p.Guidelines))
	}
	s.reply(ctx, b, update, sb.String())
}

func (s *BotService) handleTrust(ctx context.Context, b *bot.Bot, update *models.Update) {
	slug, guidelines, ok := parseKeyValueCommand(update.Message.Text, "/trust")
	if !ok {
		// Allow "/trust <slug>" without guidelines
		rest := strings.TrimSpace(strings.TrimPrefix(update.Message.Text, "/trust"))
		if rest == "" || strings.Contains(rest, " ") {
			s.reply(ctx, b, update, "Usage: /trust <slug> [guidelines]")
			return
		}
		slug = rest
		guidelines = ""
	}
	if _, err := s.repo.GetOrCreatePattern(ctx, slug, slug); err != nil {
		s.reply(ctx, b, update, "Error: "+err.Error())
		return
	}
	if err := s.repo.SetPatternTrust(ctx, slug, true, guidelines); err != nil {
		s.reply(ctx, b, update, "Error: "+err.Error())
		return
	}
	s.reply(ctx, b, update, fmt.Sprintf("🛡 Pattern %q trusted for auto-publish.", slug))
}

func (s *BotService) handleRun(ctx context.Context, b *bot.Bot, update *models.Update) {
	s.reply(ctx, b, update, "🚀 Running scheduled curation pipeline now...")
	go func(chatID int64) {
		runCtx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		if err := s.intake.HandleRunNow(runCtx); err != nil {
			_, _ = b.SendMessage(context.Background(), &bot.SendMessageParams{
				ChatID: chatID,
				Text:   "❌ Run failed: " + err.Error(),
			})
		}
	}(update.Message.Chat.ID)
}

func (s *BotService) handleDevlog(ctx context.Context, b *bot.Bot, update *models.Update) {
	s.reply(ctx, b, update, "🛠 Collecting GitHub & Codeberg commits from the last 7 days...")
	go func(chatID int64) {
		runCtx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		if err := s.intake.HandleWeeklyDevlog(runCtx); err != nil {
			_, _ = b.SendMessage(context.Background(), &bot.SendMessageParams{
				ChatID: chatID,
				Text:   "❌ Devlog failed: " + err.Error(),
			})
		}
	}(update.Message.Chat.ID)
}

func (s *BotService) handleInsights(ctx context.Context, b *bot.Bot, update *models.Update) {
	report, err := s.intake.HandleInsights(ctx)
	if err != nil {
		s.reply(ctx, b, update, "❌ Insights failed: "+err.Error())
		return
	}
	s.reply(ctx, b, update, report)
}

func (s *BotService) handleAuth(ctx context.Context, b *bot.Bot, update *models.Update) {
	if s.oauth == nil {
		s.reply(ctx, b, update, "OAuth service unavailable.")
		return
	}
	authURL, state, err := s.oauth.BuildAuthURL(ctx)
	if err != nil {
		s.reply(ctx, b, update, "Cannot start OAuth2: "+err.Error())
		return
	}
	s.reply(ctx, b, update, "🔗 Open this URL to authorize LinkedIn (valid for 5m):\n\n"+authURL)

	go func(chatID int64) {
		authCtx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		tok, err := s.oauth.RunCallbackServer(authCtx, state, 5*time.Minute)
		if err != nil && !errors.Is(err, context.Canceled) {
			_, _ = b.SendMessage(context.Background(), &bot.SendMessageParams{
				ChatID: chatID,
				Text:   "❌ LinkedIn OAuth2 failed: " + err.Error(),
			})
			return
		}
		if tok != nil {
			_, _ = b.SendMessage(context.Background(), &bot.SendMessageParams{
				ChatID: chatID,
				Text:   fmt.Sprintf("✅ LinkedIn connected (%s), expires %s.", tok.PersonURN, tok.ExpiresAt.Format("2006-01-02")),
			})
		}
	}(update.Message.Chat.ID)
}

func (s *BotService) handleDefault(ctx context.Context, b *bot.Bot, update *models.Update) {
	if update.Message == nil || update.Message.From == nil {
		return
	}
	ok, _, err := s.IsAuthorized(ctx, update.Message.From.ID)
	if err != nil || !ok {
		return
	}

	if len(update.Message.Photo) > 0 {
		// Pick highest-resolution photo (last element)
		fileID := update.Message.Photo[len(update.Message.Photo)-1].FileID
		caption := strings.TrimSpace(update.Message.Caption)
		chatID := update.Message.Chat.ID
		s.reply(ctx, b, update, "📊 Reading LinkedIn Analytics screenshot...")
		go func() {
			runCtx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
			defer cancel()
			imgBytes, err := s.DownloadFile(runCtx, fileID)
			if err != nil {
				_, _ = b.SendMessage(context.Background(), &bot.SendMessageParams{ChatID: chatID, Text: "❌ " + err.Error()})
				return
			}
			if err := s.intake.HandlePhoto(runCtx, imgBytes, caption); err != nil {
				_, _ = b.SendMessage(context.Background(), &bot.SendMessageParams{ChatID: chatID, Text: "❌ " + err.Error()})
			}
		}()
		return
	}

	if update.Message.Voice != nil {
		fileID := update.Message.Voice.FileID
		chatID := update.Message.Chat.ID
		go func() {
			runCtx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			if err := s.intake.HandleVoice(runCtx, fileID); err != nil {
				_, _ = b.SendMessage(context.Background(), &bot.SendMessageParams{
					ChatID: chatID,
					Text:   "❌ " + err.Error(),
				})
			}
		}()
		return
	}

	text := strings.TrimSpace(update.Message.Text)
	if text == "" {
		return
	}
	chatID := update.Message.Chat.ID
	if strings.HasPrefix(text, "http://") || strings.HasPrefix(text, "https://") {
		go func() {
			runCtx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			if err := s.intake.HandleLink(runCtx, text); err != nil {
				_, _ = b.SendMessage(context.Background(), &bot.SendMessageParams{
					ChatID: chatID,
					Text:   "❌ " + err.Error(),
				})
			}
		}()
		return
	}
	go func() {
		runCtx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		if err := s.intake.HandleTextIdea(runCtx, text); err != nil {
			_, _ = b.SendMessage(context.Background(), &bot.SendMessageParams{
				ChatID: chatID,
				Text:   "❌ " + err.Error(),
			})
		}
	}()
}

func (s *BotService) reply(ctx context.Context, b *bot.Bot, update *models.Update, text string) {
	if update.Message == nil {
		return
	}
	_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID: update.Message.Chat.ID,
		Text:   text,
	})
}

func parseKeyValueCommand(text, cmd string) (string, string, bool) {
	rest := strings.TrimSpace(strings.TrimPrefix(text, cmd))
	// Support both "/set key value", "/set key = value", and "/set key=value"
	if eqIdx := strings.Index(rest, "="); eqIdx > 0 {
		spIdx := strings.Index(rest, " ")
		if spIdx == -1 || eqIdx < spIdx {
			k := strings.TrimSpace(rest[:eqIdx])
			v := strings.TrimSpace(rest[eqIdx+1:])
			if k != "" && v != "" {
				return k, v, true
			}
		}
	}
	parts := strings.SplitN(rest, " ", 2)
	if len(parts) < 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
		return "", "", false
	}
	k := strings.TrimSpace(parts[0])
	v := strings.TrimSpace(parts[1])
	if strings.HasPrefix(v, "= ") || v == "=" {
		v = strings.TrimSpace(strings.TrimPrefix(v, "="))
	}
	if v == "" {
		return "", "", false
	}
	return k, v, true
}

func toggleLabel(name string, on bool) string {
	if on {
		return "✅ " + name + ": ON"
	}
	return "⬜ " + name + ": OFF"
}

func currentMonday() string {
	now := time.Now()
	offset := (int(now.Weekday()) + 6) % 7
	return now.AddDate(0, 0, -offset).Format("2006-01-02")
}
