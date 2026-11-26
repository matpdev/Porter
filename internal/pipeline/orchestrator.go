package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"codeberg.org/matheus/porter/internal/agents"
	"codeberg.org/matheus/porter/internal/odradek"
	"codeberg.org/matheus/porter/internal/odradek/curator"
	"codeberg.org/matheus/porter/internal/settings"
	"codeberg.org/matheus/porter/internal/store"
	"github.com/google/uuid"
)

const maxQAAttempts = 3

type CuratorPort interface {
	CurateFromSchedule(ctx context.Context) (*curator.CuratedTopic, error)
	CurateWeeklyDevlog(ctx context.Context) (*curator.CuratedTopic, error)
	CurateFromText(ctx context.Context, text string) (*curator.CuratedTopic, error)
	CurateFromLink(ctx context.Context, rawURL string) (*curator.CuratedTopic, error)
	CurateFromVoice(ctx context.Context, oggBytes []byte) (*curator.CuratedTopic, error)
}

type WriterPort interface {
	DraftPost(ctx context.Context, in agents.WriterInput) (*agents.PostDraft, error)
	DraftSidequestArticle(ctx context.Context, in agents.WriterInput, draft *agents.PostDraft) (*agents.SidequestArticle, error)
}

type RendererPort interface {
	Render(ctx context.Context, postID string, media agents.PostMedia) (string, error)
}

type QAPort interface {
	Review(ctx context.Context, topicSummary string, draft *agents.PostDraft, pngPath string) (*agents.QAReview, error)
}

type AnalyticsPort interface {
	ExtractFromImage(ctx context.Context, imgBytes []byte) (*odradek.ExtractedMetrics, error)
}

type LinkedInPort interface {
	PublishPost(ctx context.Context, postID, text, pngPath string) (string, error)
}

type SidequestPort interface {
	PublishArticle(ctx context.Context, article *agents.SidequestArticle, coverPNGPath string) (string, error)
}

type TelegramPort interface {
	SendAlert(ctx context.Context, text string) error
	SendPreview(ctx context.Context, postID, imagePath, title, fullText, uncertaintyReason string) (int, error)
	DownloadFile(ctx context.Context, fileID string) ([]byte, error)
}

type Orchestrator struct {
	repo      *store.Repository
	settings  *settings.Manager
	curator   CuratorPort
	writer    WriterPort
	renderer  RendererPort
	qa        QAPort
	analytics AnalyticsPort
	linkedin  LinkedInPort
	sidequest SidequestPort
	telegram  TelegramPort
	logger    *slog.Logger
}

func NewOrchestrator(
	repo *store.Repository,
	mgr *settings.Manager,
	cur CuratorPort,
	writer WriterPort,
	rend RendererPort,
	qa QAPort,
	li LinkedInPort,
	sq SidequestPort,
	tg TelegramPort,
	logger *slog.Logger,
) *Orchestrator {
	if logger == nil {
		logger = slog.Default()
	}
	return &Orchestrator{
		repo:      repo,
		settings:  mgr,
		curator:   cur,
		writer:    writer,
		renderer:  rend,
		qa:        qa,
		linkedin:  li,
		sidequest: sq,
		telegram:  tg,
		logger:    logger,
	}
}

func (o *Orchestrator) WithAnalytics(a AnalyticsPort) *Orchestrator {
	o.analytics = a
	return o
}

// --- telegram.IntakeHandler implementation ---

func (o *Orchestrator) HandleTextIdea(ctx context.Context, text string) error {
	// If user clicked [Edit] on a post, this text updates that post instead of creating a new one.
	if editing, err := o.repo.GetLatestEditingPost(ctx); err == nil && editing != nil {
		if err := o.repo.UpdatePostContentText(ctx, editing.ID, text); err != nil {
			return err
		}
		if o.telegram != nil {
			msgID, err := o.telegram.SendPreview(ctx, editing.ID, editing.MediaPath, "Edited Post Preview", text, editing.UncertaintyReason)
			if err == nil {
				_ = o.repo.UpdatePostPreviewMsgID(ctx, editing.ID, msgID)
			}
		}
		return nil
	}

	topic, err := o.curator.CurateFromText(ctx, text)
	if err != nil {
		return err
	}
	if o.telegram != nil {
		_ = o.telegram.SendAlert(ctx, "🔍 Odradek started drafting from your note...")
	}
	return o.RunTopic(ctx, *topic, false)
}

func (o *Orchestrator) HandleLink(ctx context.Context, rawURL string) error {
	topic, err := o.curator.CurateFromLink(ctx, rawURL)
	if err != nil {
		return err
	}
	if o.telegram != nil {
		_ = o.telegram.SendAlert(ctx, "🔍 Odradek extracted the article and started drafting...")
	}
	return o.RunTopic(ctx, *topic, false)
}

func (o *Orchestrator) HandleVoice(ctx context.Context, fileID string) error {
	if o.telegram == nil {
		return errors.New("telegram unavailable")
	}
	oggBytes, err := o.telegram.DownloadFile(ctx, fileID)
	if err != nil {
		return err
	}
	topic, err := o.curator.CurateFromVoice(ctx, oggBytes)
	if err != nil {
		return err
	}
	_ = o.telegram.SendAlert(ctx, fmt.Sprintf("🎙 Transcribed: %q\nDrafting now...", topic.Title))
	return o.RunTopic(ctx, *topic, false)
}

func (o *Orchestrator) HandlePostAction(ctx context.Context, action, postID string) error {
	post, err := o.repo.GetPostByID(ctx, postID)
	if err != nil {
		return err
	}

	switch action {
	case "approve":
		if post.PatternSlug != nil && *post.PatternSlug != "" {
			threshold, err := o.settings.GetInt(ctx, settings.KeyAutoTrustAfterApprovals)
			if err != nil {
				threshold = 2
			}
			_, _ = o.repo.IncrementPatternApproval(ctx, *post.PatternSlug, threshold)
		}
		return o.deliverPost(ctx, post)

	case "trust":
		if post.PatternSlug != nil && *post.PatternSlug != "" {
			if _, err := o.repo.GetOrCreatePattern(ctx, *post.PatternSlug, *post.PatternSlug); err == nil {
				_ = o.repo.SetPatternTrust(ctx, *post.PatternSlug, true, "")
			}
		}
		return o.deliverPost(ctx, post)

	case "edit":
		if err := o.repo.UpdatePostStatus(ctx, postID, "EDITING", ""); err != nil {
			return err
		}
		if o.telegram != nil {
			_ = o.telegram.SendAlert(ctx, "✏️ Send the revised post text as your next message.")
		}
		return nil

	case "discard":
		if post.WeeklyThemeID != nil {
			_ = o.repo.UpdateWeeklyThemeStatus(ctx, *post.WeeklyThemeID, "SKIPPED", &post.ID)
		}
		return o.repo.UpdatePostStatus(ctx, postID, "DISCARDED", "")

	case "regen":
		topic := curator.CuratedTopic{
			WeeklyThemeID:   post.WeeklyThemeID,
			PatternSlug:     post.PatternSlug,
			SourceType:      "telegram_text",
			Title:           "Regenerated Post",
			Summary:         post.ContentText,
			Angle:           "Rewrite with a sharper engineering angle and cleaner structure.",
			TargetLinkedIn:  post.PublishToLinkedIn,
			TargetSidequest: post.PublishToSidequest,
		}
		return o.RunTopic(ctx, topic, false)

	default:
		return fmt.Errorf("unknown action %q", action)
	}
}

// RunScheduled executes one periodic curation + drafting + QA + conditional auto-publish cycle.
func (o *Orchestrator) RunScheduled(ctx context.Context) error {
	topic, err := o.curator.CurateFromSchedule(ctx)
	if err != nil {
		return err
	}
	return o.RunTopic(ctx, *topic, true)
}

func (o *Orchestrator) HandleRunNow(ctx context.Context) error {
	return o.RunScheduled(ctx)
}

func (o *Orchestrator) HandleWeeklyDevlog(ctx context.Context) error {
	topic, err := o.curator.CurateWeeklyDevlog(ctx)
	if err != nil {
		return err
	}
	return o.RunTopic(ctx, *topic, false)
}

func (o *Orchestrator) HandlePhoto(ctx context.Context, imgBytes []byte, caption string) error {
	if o.analytics == nil {
		return errors.New("analytics extractor not configured")
	}
	metrics, err := o.analytics.ExtractFromImage(ctx, imgBytes)
	if err != nil {
		return err
	}

	post, err := o.repo.GetLatestPublishedPost(ctx)
	if err != nil {
		return fmt.Errorf("no published LinkedIn post found to attach metrics: %w", err)
	}

	notes := metrics.Notes
	if caption != "" {
		notes = caption + " | " + notes
	}
	if err := o.repo.InsertPostMetric(ctx, store.PostMetric{
		PostID:      post.ID,
		Impressions: metrics.Impressions,
		Reactions:   metrics.Reactions,
		Comments:    metrics.Comments,
		Reposts:     metrics.Reposts,
		RawNotes:    notes,
	}); err != nil {
		return err
	}

	if o.telegram != nil {
		msg := fmt.Sprintf(
			"📈 Metrics saved for post %s:\n• Impressions: %d\n• Reactions: %d\n• Comments: %d\n• Reposts: %d\n• Notes: %s\n\nRun /insights to see trends.",
			post.ID[:8], metrics.Impressions, metrics.Reactions, metrics.Comments, metrics.Reposts, metrics.Notes,
		)
		_ = o.telegram.SendAlert(ctx, msg)
	}
	return nil
}

func (o *Orchestrator) HandleInsights(ctx context.Context) (string, error) {
	sum, err := o.repo.GetAnalyticsSummary(ctx)
	if err != nil {
		return "", err
	}
	return odradek.FormatInsightsReport(sum), nil
}

func (o *Orchestrator) RunTopic(ctx context.Context, topic curator.CuratedTopic, isScheduled bool) error {
	postID := uuid.NewString()
	var topicIDPtr *int64
	if topic.TopicID > 0 {
		topicIDPtr = &topic.TopicID
	}

	post := store.Post{
		ID:                  postID,
		TopicID:             topicIDPtr,
		WeeklyThemeID:       topic.WeeklyThemeID,
		PatternSlug:         topic.PatternSlug,
		Status:              "DRAFTING",
		RequiresHumanReview: true,
		PublishToLinkedIn:   topic.TargetLinkedIn,
		PublishToSidequest:  topic.TargetSidequest,
	}
	if err := o.repo.CreatePost(ctx, post); err != nil {
		return err
	}

	var patternGuidelines string
	if topic.PatternSlug != nil && *topic.PatternSlug != "" {
		if pat, err := o.repo.GetPattern(ctx, *topic.PatternSlug); err == nil {
			patternGuidelines = pat.Guidelines
		}
	}

	var (
		draft    *agents.PostDraft
		review   *agents.QAReview
		pngPath  string
		feedback string
	)

	for attempt := 1; attempt <= maxQAAttempts; attempt++ {
		o.logger.Info("pipeline drafting", "post_id", postID, "attempt", attempt, "title", topic.Title)
		post.AttemptCount = attempt
		var err error
		draft, err = o.writer.DraftPost(ctx, agents.WriterInput{
			Topic:             topic,
			PatternGuidelines: patternGuidelines,
			PreviousFeedback:  feedback,
			Attempt:           attempt,
		})
		if err != nil {
			o.logger.Error("writer failed", "post_id", postID, "err", err)
			_ = o.repo.UpdatePostStatus(ctx, postID, "FAILED", err.Error())
			return err
		}

		if draft.Media.Type != agents.MediaNone && o.renderer != nil {
			o.logger.Info("rendering media", "post_id", postID, "type", draft.Media.Type)
			pngPath, err = o.renderer.Render(ctx, postID, draft.Media)
			if err != nil {
				o.logger.Warn("render failed, continuing without image", "err", err)
				pngPath = ""
			}
		}

		o.logger.Info("running odradek QA", "post_id", postID, "attempt", attempt, "has_png", pngPath != "")
		review, err = o.qa.Review(ctx, topic.Summary, draft, pngPath)
		if err != nil {
			o.logger.Error("qa failed", "post_id", postID, "err", err)
			_ = o.repo.UpdatePostStatus(ctx, postID, "FAILED", err.Error())
			return err
		}

		draftJSON, _ := json.Marshal(draft)
		qaJSON, _ := json.Marshal(review)
		_ = o.repo.InsertQALog(ctx, store.QALog{
			PostID:        postID,
			AttemptNumber: attempt,
			DraftSnapshot: string(draftJSON),
			QAApproved:    review.Approved,
			QAResponse:    string(qaJSON),
		})

		if review.Approved {
			break
		}
		feedback = review.SummaryFeedback
	}

	patternSlug := draft.TopicPatternSlug
	if topic.PatternSlug != nil && *topic.PatternSlug != "" {
		patternSlug = *topic.PatternSlug
	}
	pat, _ := o.repo.GetOrCreatePattern(ctx, patternSlug, draft.TopicPatternLabel)

	minConf, err := o.settings.GetInt(ctx, settings.KeyMinConfidenceScore)
	if err != nil {
		minConf = 8
	}
	tgReqApproval, err := o.settings.GetBool(ctx, settings.KeyTelegramRequireApproval)
	if err != nil {
		tgReqApproval = true
	}

	needsReview, reason := o.evaluateHumanGate(pat, draft, review, minConf, isScheduled, tgReqApproval)

	post.PatternSlug = &patternSlug
	post.RequiresHumanReview = needsReview
	post.UncertaintyReason = reason
	post.ContentText = draft.FormatForLinkedIn()
	post.MediaType = string(draft.Media.Type)
	post.MediaPayload = draft.Media.Content
	post.MediaLanguage = draft.Media.Language
	post.MediaPath = pngPath

	if needsReview {
		post.Status = "AWAITING_APPROVAL"
		if err := o.repo.UpdatePostDraftResult(ctx, post); err != nil {
			return err
		}
		if o.telegram != nil {
			msgID, err := o.telegram.SendPreview(ctx, post.ID, pngPath, draft.Media.Title, post.ContentText, reason)
			if err == nil {
				_ = o.repo.UpdatePostPreviewMsgID(ctx, post.ID, msgID)
			}
		}
		return nil
	}

	post.Status = "PUBLISHING"
	if err := o.repo.UpdatePostDraftResult(ctx, post); err != nil {
		return err
	}
	return o.deliverLoadedPost(ctx, &post, topic, draft)
}

func (o *Orchestrator) evaluateHumanGate(
	pat *store.TopicPattern,
	draft *agents.PostDraft,
	review *agents.QAReview,
	minConf int,
	isScheduled bool,
	tgReqApproval bool,
) (bool, string) {
	if !review.Approved {
		return true, "Odradek QA did not approve after 3 attempts: " + review.SummaryFeedback
	}
	if draft.HasUncertainty {
		return true, "Writer uncertainty: " + draft.UncertaintyReason
	}
	if review.HasUncertainty {
		return true, "Odradek QA uncertainty: " + review.UncertaintyReason
	}
	if review.ConfidenceScore < minConf {
		return true, fmt.Sprintf("Odradek confidence %d/%d is below threshold (%d).", review.ConfidenceScore, 10, minConf)
	}
	if pat == nil || !pat.AutoPublish {
		slug := ""
		if pat != nil {
			slug = pat.Slug
		}
		return true, fmt.Sprintf("New or untrusted topic pattern (%s). Approve + Trust to allow future auto-publish.", slug)
	}
	if !isScheduled && tgReqApproval {
		return true, "On-demand Telegram draft awaiting your approval."
	}
	return false, ""
}

func (o *Orchestrator) deliverPost(ctx context.Context, post *store.Post) error {
	return o.deliverLoadedPost(ctx, post, curator.CuratedTopic{
		Title:           "Approved Post",
		Summary:         post.ContentText,
		TargetLinkedIn:  post.PublishToLinkedIn,
		TargetSidequest: post.PublishToSidequest,
	}, nil)
}

func (o *Orchestrator) deliverLoadedPost(ctx context.Context, post *store.Post, topic curator.CuratedTopic, draft *agents.PostDraft) error {
	var liURN, sqPath string

	if post.PublishToLinkedIn && o.linkedin != nil {
		urn, err := o.linkedin.PublishPost(ctx, post.ID, post.ContentText, post.MediaPath)
		if err != nil {
			_ = o.repo.UpdatePostStatus(ctx, post.ID, "FAILED", err.Error())
			if o.telegram != nil {
				_ = o.telegram.SendAlert(ctx, "❌ LinkedIn publish failed: "+err.Error())
			}
			return err
		}
		liURN = urn
	}

	sqEnabled, _ := o.settings.GetBool(ctx, settings.KeySidequestEnabled)
	if (post.PublishToSidequest || sqEnabled) && o.sidequest != nil && o.writer != nil {
		article, err := o.writer.DraftSidequestArticle(ctx, agents.WriterInput{Topic: topic}, draft)
		if err != nil {
			o.logger.Error("sidequest article draft failed", "post_id", post.ID, "err", err)
			if o.telegram != nil {
				_ = o.telegram.SendAlert(ctx, "⚠️ LinkedIn OK, but sidequestlab.dev draft failed: "+err.Error())
			}
		} else {
			p, err := o.sidequest.PublishArticle(ctx, article, post.MediaPath)
			if err != nil {
				o.logger.Error("sidequest publish failed", "post_id", post.ID, "err", err)
				if o.telegram != nil {
					_ = o.telegram.SendAlert(ctx, "⚠️ sidequestlab.dev publish error: "+err.Error())
				}
			} else {
				sqPath = p
			}
		}
	}

	if err := o.repo.MarkPublished(ctx, post.ID, liURN, sqPath); err != nil {
		return err
	}
	if post.WeeklyThemeID != nil {
		_ = o.repo.UpdateWeeklyThemeStatus(ctx, *post.WeeklyThemeID, "COMPLETED", &post.ID)
	}

	if o.telegram != nil {
		msg := fmt.Sprintf("📦 Porter delivered post %s\n• LinkedIn: %s", post.ID[:8], liURN)
		if liURN != "" && !strings.HasPrefix(liURN, "urn:li:dryrun:") {
			msg += fmt.Sprintf("\n• Link: https://www.linkedin.com/feed/update/%s/", liURN)
		}
		if sqPath != "" {
			msg += "\n• sidequestlab.dev: " + sqPath
		}
		_ = o.telegram.SendAlert(ctx, msg)
	}
	return nil
}

// SendMetricsReminders notifies the user every X days to screenshot LinkedIn Analytics for recent posts.
func (o *Orchestrator) SendMetricsReminders(ctx context.Context) error {
	if o.telegram == nil {
		return nil
	}
	days, err := o.settings.GetInt(ctx, settings.KeyAnalyticsReminderDays)
	if err != nil || days <= 0 {
		days = 3
	}
	posts, err := o.repo.ListPostsNeedingMetricsReminder(ctx, days)
	if err != nil || len(posts) == 0 {
		return err
	}

	var lines []string
	for _, p := range posts {
		lines = append(lines, fmt.Sprintf("• https://www.linkedin.com/feed/update/%s/", p.LinkedInPostURN))
		_ = o.repo.TouchMetricsReminder(ctx, p.ID)
	}
	msg := fmt.Sprintf("📊 Time to check growth (%dd+ since publish)!\nOpen the post(s) below, take an Analytics screenshot, and send the image right here:\n\n%s",
		days, strings.Join(lines, "\n"))
	return o.telegram.SendAlert(ctx, msg)
}

// SendPendingReminders pings the user about posts awaiting review due to new topics or doubts.
func (o *Orchestrator) SendPendingReminders(ctx context.Context) error {
	if o.telegram == nil {
		return nil
	}
	posts, err := o.repo.ListPendingApprovalPosts(ctx)
	if err != nil || len(posts) == 0 {
		return err
	}

	now := time.Now().UTC()
	var lines []string
	var remindedIDs []string

	for _, p := range posts {
		if p.LastReminderAt != nil && now.Sub(*p.LastReminderAt) < 6*time.Hour {
			continue
		}
		reason := p.UncertaintyReason
		if reason == "" {
			reason = "Awaiting manual review"
		}
		lines = append(lines, fmt.Sprintf("• [%s] %s", p.ID[:8], reason))
		remindedIDs = append(remindedIDs, p.ID)
	}
	if len(lines) == 0 {
		return nil
	}

	msg := "🔔 Odradek Review Reminder:\n" + strings.Join(lines, "\n")
	if err := o.telegram.SendAlert(ctx, msg); err != nil {
		return err
	}
	for _, id := range remindedIDs {
		_ = o.repo.TouchPostReminder(ctx, id)
	}
	return nil
}
