package curator

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"codeberg.org/matheus/porter/internal/settings"
	"codeberg.org/matheus/porter/internal/store"
)

var ErrNoFreshTopics = errors.New("no fresh unseen topics available from feeds")

type Service struct {
	repo      *store.Repository
	settings  *settings.Manager
	extractor *Extractor
	feeds     *FeedCollector
	git       *GitActivityCollector
	inspector *RepoInspector
	audio     *AudioTranscriber
}

func NewService(
	repo *store.Repository,
	mgr *settings.Manager,
	extractor *Extractor,
	feeds *FeedCollector,
	git *GitActivityCollector,
	audio *AudioTranscriber,
) *Service {
	if extractor == nil {
		extractor = NewExtractor(nil)
	}
	if feeds == nil {
		feeds = NewFeedCollector(nil)
	}
	if git == nil {
		git = NewGitActivityCollector(nil)
	}
	if audio == nil {
		audio = NewAudioTranscriber(mgr)
	}
	return &Service{
		repo:      repo,
		settings:  mgr,
		extractor: extractor,
		feeds:     feeds,
		git:       git,
		inspector: NewRepoInspector(nil),
		audio:     audio,
	}
}

func (s *Service) WithRepoInspector(ri *RepoInspector) *Service {
	if ri != nil {
		s.inspector = ri
	}
	return s
}

// CurateFromSchedule prioritizes: 1) weekly_themes, 2) unseen GitHub/Codeberg weekly devlog, 3) HN & RSS feeds.
func (s *Service) CurateFromSchedule(ctx context.Context) (*CuratedTopic, error) {
	monday := currentMonday()

	theme, err := s.repo.GetNextPlannedTheme(ctx, monday)
	if err == nil && theme != nil {
		_ = s.repo.UpdateWeeklyThemeStatus(ctx, theme.ID, "IN_PROGRESS", nil)
		summary := theme.Angle
		if theme.SourceURL != "" {
			if _, extracted, err := s.extractor.Extract(ctx, theme.SourceURL); err == nil && extracted != "" {
				summary = extracted
			}
		}
		var urlHash *string
		if theme.SourceURL != "" {
			if norm, err := NormalizeURL(theme.SourceURL); err == nil {
				h := HashURL(norm)
				urlHash = &h
			}
		}
		topicID, err := s.repo.SaveTopic(ctx, store.Topic{
			SourceType: "weekly_theme",
			SourceURL:  theme.SourceURL,
			URLHash:    urlHash,
			Title:      theme.Title,
			Summary:    summary,
		})
		if err != nil {
			return nil, err
		}
		return &CuratedTopic{
			TopicID:         topicID,
			WeeklyThemeID:   &theme.ID,
			SourceType:      "weekly_theme",
			SourceURL:       theme.SourceURL,
			Title:           theme.Title,
			Summary:         summary,
			Angle:           theme.Angle,
			PatternSlug:     theme.PatternSlug,
			TargetLinkedIn:  theme.TargetLinkedIn,
			TargetSidequest: theme.TargetSidequest,
		}, nil
	}
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}

	// Check if we haven't generated a weekly GitHub/Codeberg devlog for this week yet.
	devlogHash := HashURL("git_weekly:" + monday)
	if processed, err := s.repo.IsURLProcessed(ctx, devlogHash); err == nil && !processed {
		if topic, err := s.curateWeeklyDevlogWithHash(ctx, &devlogHash); err == nil {
			return topic, nil
		}
	}

	return s.CurateFromFeeds(ctx)
}

func (s *Service) CurateWeeklyDevlog(ctx context.Context) (*CuratedTopic, error) {
	return s.curateWeeklyDevlogWithHash(ctx, nil)
}

func (s *Service) curateWeeklyDevlogWithHash(ctx context.Context, urlHash *string) (*CuratedTopic, error) {
	ghUser, _ := s.settings.GetString(ctx, settings.KeyCuratorGitHubUsername)
	ghToken, _ := s.settings.GetString(ctx, settings.KeyCuratorGitHubToken)
	cbUser, _ := s.settings.GetString(ctx, settings.KeyCuratorCodebergUsername)
	cbToken, _ := s.settings.GetString(ctx, settings.KeyCuratorCodebergToken)
	reposRaw, _ := s.settings.GetString(ctx, settings.KeyCuratorGitRepos)

	if strings.TrimSpace(ghUser) == "" && strings.TrimSpace(cbUser) == "" {
		return nil, errors.New("no github or codeberg username configured")
	}

	var repos []string
	for _, r := range strings.Split(reposRaw, ",") {
		if t := strings.TrimSpace(r); t != "" {
			repos = append(repos, t)
		}
	}

	updates, err := s.git.CollectWeeklySummary(ctx, GitSummaryConfig{
		GitHubUsername:   strings.TrimSpace(ghUser),
		GitHubToken:      strings.TrimSpace(ghToken),
		CodebergUsername: strings.TrimSpace(cbUser),
		CodebergToken:    strings.TrimSpace(cbToken),
		Repos:            repos,
		Since:            time.Now().UTC().AddDate(0, 0, -7),
	})
	if err != nil {
		return nil, err
	}
	if len(updates) == 0 {
		return nil, errors.New("no recent commits found in the last 7 days")
	}

	summary := FormatRepoUpdates(updates)
	title := fmt.Sprintf("Weekly Engineering Update (%s)", currentMonday())
	sqEnabled, _ := s.settings.GetBool(ctx, settings.KeySidequestEnabled)

	topicID, err := s.repo.SaveTopic(ctx, store.Topic{
		SourceType: "git_weekly",
		URLHash:    urlHash,
		Title:      title,
		Summary:    summary,
	})
	if err != nil {
		return nil, err
	}

	return &CuratedTopic{
		TopicID:         topicID,
		SourceType:      "git_weekly",
		Title:           title,
		Summary:         summary,
		Angle:           "Share concrete technical updates, tradeoffs, and what was shipped across these repositories this week.",
		TargetLinkedIn:  true,
		TargetSidequest: sqEnabled,
	}, nil
}

func (s *Service) CurateFromFeeds(ctx context.Context) (*CuratedTopic, error) {
	minScore, err := s.settings.GetInt(ctx, settings.KeyCuratorHNMinScore)
	if err != nil {
		minScore = 80
	}
	feedsRaw, _ := s.settings.GetString(ctx, settings.KeyCuratorRSSFeeds)
	feedURLs := strings.Split(feedsRaw, ",")

	var candidates []FeedCandidate
	if hn, err := s.feeds.FetchHackerNews(ctx, minScore, 20); err == nil {
		candidates = append(candidates, hn...)
	}
	candidates = append(candidates, s.feeds.FetchRSS(ctx, feedURLs, 10)...)

	sqEnabled, _ := s.settings.GetBool(ctx, settings.KeySidequestEnabled)

	for _, c := range candidates {
		norm, err := NormalizeURL(c.URL)
		if err != nil {
			continue
		}
		h := HashURL(norm)
		seen, err := s.repo.IsURLProcessed(ctx, h)
		if err != nil || seen {
			continue
		}

		title := c.Title
		summary := c.Description
		if extTitle, extText, err := s.extractor.Extract(ctx, norm); err == nil && extText != "" {
			if extTitle != "" {
				title = extTitle
			}
			summary = extText
		}

		topicID, err := s.repo.SaveTopic(ctx, store.Topic{
			SourceType: c.SourceType,
			SourceURL:  norm,
			URLHash:    &h,
			Title:      title,
			Summary:    summary,
		})
		if err != nil {
			continue
		}

		return &CuratedTopic{
			TopicID:         topicID,
			SourceType:      c.SourceType,
			SourceURL:       norm,
			Title:           title,
			Summary:         summary,
			TargetLinkedIn:  true,
			TargetSidequest: sqEnabled,
		}, nil
	}

	return nil, ErrNoFreshTopics
}

func (s *Service) CurateFromText(ctx context.Context, text string) (*CuratedTopic, error) {
	sqEnabled, _ := s.settings.GetBool(ctx, settings.KeySidequestEnabled)
	title := strings.TrimSpace(text)
	if len(title) > 80 {
		title = title[:80] + "..."
	}

	summary := text
	sourceURL := ""
	if req, ok := ParseRepoIntent(text); ok {
		ghToken, _ := s.settings.GetString(ctx, settings.KeyCuratorGitHubToken)
		cbToken, _ := s.settings.GetString(ctx, settings.KeyCuratorCodebergToken)

		tryInspect := func(r RepoRequest) (string, string, error) {
			tok := strings.TrimSpace(ghToken)
			if r.Forge == "codeberg" {
				tok = strings.TrimSpace(cbToken)
			}
			return s.inspector.Inspect(ctx, r, tok)
		}

		repoURL, repoSummary, err := tryInspect(req)
		if err != nil && !req.ForgeSpecified {
			// Fallback to the other forge when user didn't specify GitHub vs Codeberg.
			alt := req
			if req.Forge == "github" {
				alt.Forge = "codeberg"
			} else {
				alt.Forge = "github"
			}
			if u, sum, altErr := tryInspect(alt); altErr == nil {
				repoURL, repoSummary, err = u, sum, nil
			}
		}

		if err == nil && repoSummary != "" {
			sourceURL = repoURL
			summary = fmt.Sprintf("User request: %s\n\n%s", text, repoSummary)
			title = fmt.Sprintf("%s (%s)", req.FullName(), req.Branch)
			if req.Branch == "" {
				title = req.FullName()
			}
		}
	} else if strings.Contains(strings.ToLower(text), "github") || strings.Contains(strings.ToLower(text), "codeberg") {
		// Language-agnostic fallback: if user mentions GitHub or Codeberg without owner/repo, pull recent activity.
		if devlog, err := s.curateWeeklyDevlogWithHash(ctx, nil); err == nil && devlog.Summary != "" {
			summary = fmt.Sprintf("User request: %s\n\n%s", text, devlog.Summary)
		}
	}

	topicID, err := s.repo.SaveTopic(ctx, store.Topic{
		SourceType: "telegram_text",
		SourceURL:  sourceURL,
		Title:      title,
		Summary:    summary,
	})
	if err != nil {
		return nil, err
	}
	return &CuratedTopic{
		TopicID:         topicID,
		SourceType:      "telegram_text",
		SourceURL:       sourceURL,
		Title:           title,
		Summary:         summary,
		Angle:           text,
		TargetLinkedIn:  true,
		TargetSidequest: sqEnabled,
	}, nil
}

func (s *Service) CurateFromLink(ctx context.Context, rawURL string) (*CuratedTopic, error) {
	norm, err := NormalizeURL(rawURL)
	if err != nil {
		return nil, err
	}
	title, text, err := s.extractor.Extract(ctx, norm)
	if err != nil || title == "" {
		title = norm
	}
	if text == "" {
		text = "Reference URL: " + norm
	}

	sqEnabled, _ := s.settings.GetBool(ctx, settings.KeySidequestEnabled)
	topicID, err := s.repo.SaveTopic(ctx, store.Topic{
		SourceType: "telegram_link",
		SourceURL:  norm,
		Title:      title,
		Summary:    text,
	})
	if err != nil {
		return nil, err
	}
	return &CuratedTopic{
		TopicID:         topicID,
		SourceType:      "telegram_link",
		SourceURL:       norm,
		Title:           title,
		Summary:         text,
		TargetLinkedIn:  true,
		TargetSidequest: sqEnabled,
	}, nil
}

func (s *Service) CurateFromVoice(ctx context.Context, oggBytes []byte) (*CuratedTopic, error) {
	intent, err := s.audio.TranscribeOGG(ctx, oggBytes)
	if err != nil {
		return nil, err
	}
	sqEnabled, _ := s.settings.GetBool(ctx, settings.KeySidequestEnabled)
	targetSQ := sqEnabled || intent.TargetSidequest

	topicID, err := s.repo.SaveTopic(ctx, store.Topic{
		SourceType: "telegram_voice",
		Title:      intent.Title,
		Summary:    intent.Summary,
	})
	if err != nil {
		return nil, err
	}
	return &CuratedTopic{
		TopicID:         topicID,
		SourceType:      "telegram_voice",
		Title:           intent.Title,
		Summary:         intent.Summary,
		Angle:           intent.Angle,
		TargetLinkedIn:  true,
		TargetSidequest: targetSQ,
	}, nil
}

func currentMonday() string {
	now := time.Now()
	offset := (int(now.Weekday()) + 6) % 7
	return now.AddDate(0, 0, -offset).Format("2006-01-02")
}
