package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

var ErrNotFound = errors.New("record not found")

type Repository struct {
	db *sql.DB
}

func NewRepository(db *sql.DB) *Repository {
	return &Repository{db: db}
}

// --- Settings ---

type Setting struct {
	Key       string
	Value     string
	IsSecret  bool
	UpdatedAt time.Time
}

func (r *Repository) GetSetting(ctx context.Context, key string) (*Setting, error) {
	var s Setting
	var isSecret int
	err := r.db.QueryRowContext(ctx,
		`SELECT key, value, is_secret, updated_at FROM settings WHERE key = ?`, key,
	).Scan(&s.Key, &s.Value, &isSecret, &s.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get setting %s: %w", key, err)
	}
	s.IsSecret = isSecret == 1
	return &s, nil
}

func (r *Repository) SetSetting(ctx context.Context, key, value string, isSecret bool) error {
	sec := 0
	if isSecret {
		sec = 1
	}
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO settings (key, value, is_secret, updated_at)
		VALUES (?, ?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(key) DO UPDATE SET
			value = excluded.value,
			is_secret = CASE WHEN excluded.is_secret = 1 THEN 1 ELSE settings.is_secret END,
			updated_at = CURRENT_TIMESTAMP
	`, key, value, sec)
	if err != nil {
		return fmt.Errorf("set setting %s: %w", key, err)
	}
	return nil
}

func (r *Repository) EnsureSettingDefault(ctx context.Context, key, value string, isSecret bool) error {
	sec := 0
	if isSecret {
		sec = 1
	}
	_, err := r.db.ExecContext(ctx, `
		INSERT OR IGNORE INTO settings (key, value, is_secret, updated_at)
		VALUES (?, ?, ?, CURRENT_TIMESTAMP)
	`, key, value, sec)
	if err != nil {
		return fmt.Errorf("ensure default setting %s: %w", key, err)
	}
	return nil
}

func (r *Repository) ListSettings(ctx context.Context) ([]Setting, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT key, value, is_secret, updated_at FROM settings ORDER BY key ASC`,
	)
	if err != nil {
		return nil, fmt.Errorf("list settings: %w", err)
	}
	defer rows.Close()

	var out []Setting
	for rows.Next() {
		var s Setting
		var isSecret int
		if err := rows.Scan(&s.Key, &s.Value, &isSecret, &s.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan setting: %w", err)
		}
		s.IsSecret = isSecret == 1
		out = append(out, s)
	}
	return out, rows.Err()
}

// --- Topic Patterns (Odradek Trust & Guidelines) ---

type TopicPattern struct {
	Slug          string
	Label         string
	AutoPublish   bool
	ApprovedCount int
	Guidelines    string
	UpdatedAt     time.Time
}

func (r *Repository) GetOrCreatePattern(ctx context.Context, slug, label string) (*TopicPattern, error) {
	_, err := r.db.ExecContext(ctx, `
		INSERT OR IGNORE INTO topic_patterns (slug, label, updated_at)
		VALUES (?, ?, CURRENT_TIMESTAMP)
	`, slug, label)
	if err != nil {
		return nil, fmt.Errorf("insert pattern %s: %w", slug, err)
	}
	return r.GetPattern(ctx, slug)
}

func (r *Repository) GetPattern(ctx context.Context, slug string) (*TopicPattern, error) {
	var p TopicPattern
	var autoPub int
	err := r.db.QueryRowContext(ctx, `
		SELECT slug, label, auto_publish, approved_count, guidelines, updated_at
		FROM topic_patterns WHERE slug = ?
	`, slug).Scan(&p.Slug, &p.Label, &autoPub, &p.ApprovedCount, &p.Guidelines, &p.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get pattern %s: %w", slug, err)
	}
	p.AutoPublish = autoPub == 1
	return &p, nil
}

func (r *Repository) SetPatternTrust(ctx context.Context, slug string, autoPublish bool, guidelines string) error {
	auto := 0
	if autoPublish {
		auto = 1
	}
	res, err := r.db.ExecContext(ctx, `
		UPDATE topic_patterns
		SET auto_publish = ?, guidelines = ?, updated_at = CURRENT_TIMESTAMP
		WHERE slug = ?
	`, auto, guidelines, slug)
	if err != nil {
		return fmt.Errorf("update pattern trust %s: %w", slug, err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *Repository) IncrementPatternApproval(ctx context.Context, slug string, autoTrustThreshold int) (*TopicPattern, error) {
	_, err := r.db.ExecContext(ctx, `
		UPDATE topic_patterns
		SET approved_count = approved_count + 1,
		    auto_publish = CASE
		        WHEN ? > 0 AND (approved_count + 1) >= ? THEN 1
		        ELSE auto_publish
		    END,
		    updated_at = CURRENT_TIMESTAMP
		WHERE slug = ?
	`, autoTrustThreshold, autoTrustThreshold, slug)
	if err != nil {
		return nil, fmt.Errorf("increment pattern approval %s: %w", slug, err)
	}
	return r.GetPattern(ctx, slug)
}

func (r *Repository) ListPatterns(ctx context.Context) ([]TopicPattern, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT slug, label, auto_publish, approved_count, guidelines, updated_at
		FROM topic_patterns ORDER BY slug ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("list patterns: %w", err)
	}
	defer rows.Close()

	var out []TopicPattern
	for rows.Next() {
		var p TopicPattern
		var auto int
		if err := rows.Scan(&p.Slug, &p.Label, &auto, &p.ApprovedCount, &p.Guidelines, &p.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan pattern: %w", err)
		}
		p.AutoPublish = auto == 1
		out = append(out, p)
	}
	return out, rows.Err()
}

// --- Weekly Themes ---

type WeeklyTheme struct {
	ID              int64
	WeekStart       string
	ScheduledDay    string
	PatternSlug     *string
	Title           string
	Angle           string
	SourceURL       string
	TargetLinkedIn  bool
	TargetSidequest bool
	Status          string
	PostID          *string
	CreatedAt       time.Time
}

func (r *Repository) CreateWeeklyTheme(ctx context.Context, t WeeklyTheme) (int64, error) {
	li, sq := 0, 0
	if t.TargetLinkedIn {
		li = 1
	}
	if t.TargetSidequest {
		sq = 1
	}
	status := t.Status
	if status == "" {
		status = "PLANNED"
	}
	res, err := r.db.ExecContext(ctx, `
		INSERT INTO weekly_themes (
			week_start, scheduled_day, pattern_slug, title, angle, source_url,
			target_linkedin, target_sidequest, status
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, t.WeekStart, t.ScheduledDay, t.PatternSlug, t.Title, t.Angle, t.SourceURL, li, sq, status)
	if err != nil {
		return 0, fmt.Errorf("create weekly theme: %w", err)
	}
	return res.LastInsertId()
}

func (r *Repository) GetNextPlannedTheme(ctx context.Context, weekStart string) (*WeeklyTheme, error) {
	var t WeeklyTheme
	var li, sq int
	err := r.db.QueryRowContext(ctx, `
		SELECT id, week_start, scheduled_day, pattern_slug, title, angle, source_url,
		       target_linkedin, target_sidequest, status, post_id, created_at
		FROM weekly_themes
		WHERE week_start <= ? AND status = 'PLANNED'
		ORDER BY week_start ASC, id ASC
		LIMIT 1
	`, weekStart).Scan(
		&t.ID, &t.WeekStart, &t.ScheduledDay, &t.PatternSlug, &t.Title, &t.Angle, &t.SourceURL,
		&li, &sq, &t.Status, &t.PostID, &t.CreatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get next planned theme: %w", err)
	}
	t.TargetLinkedIn = li == 1
	t.TargetSidequest = sq == 1
	return &t, nil
}

func (r *Repository) ListWeeklyThemes(ctx context.Context, weekStart string) ([]WeeklyTheme, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, week_start, scheduled_day, pattern_slug, title, angle, source_url,
		       target_linkedin, target_sidequest, status, post_id, created_at
		FROM weekly_themes
		WHERE (? = '' OR week_start = ?)
		ORDER BY week_start DESC, id ASC
	`, weekStart, weekStart)
	if err != nil {
		return nil, fmt.Errorf("list weekly themes: %w", err)
	}
	defer rows.Close()

	var out []WeeklyTheme
	for rows.Next() {
		var t WeeklyTheme
		var li, sq int
		if err := rows.Scan(
			&t.ID, &t.WeekStart, &t.ScheduledDay, &t.PatternSlug, &t.Title, &t.Angle, &t.SourceURL,
			&li, &sq, &t.Status, &t.PostID, &t.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan weekly theme: %w", err)
		}
		t.TargetLinkedIn = li == 1
		t.TargetSidequest = sq == 1
		out = append(out, t)
	}
	return out, rows.Err()
}

func (r *Repository) UpdateWeeklyThemeStatus(ctx context.Context, id int64, status string, postID *string) error {
	res, err := r.db.ExecContext(ctx, `
		UPDATE weekly_themes SET status = ?, post_id = COALESCE(?, post_id) WHERE id = ?
	`, status, postID, id)
	if err != nil {
		return fmt.Errorf("update weekly theme %d: %w", id, err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// --- Topics History ---

type Topic struct {
	ID         int64
	SourceType string
	SourceURL  string
	URLHash    *string
	Title      string
	Keywords   string
	Summary    string
	CreatedAt  time.Time
}

func (r *Repository) IsURLProcessed(ctx context.Context, urlHash string) (bool, error) {
	var count int
	err := r.db.QueryRowContext(ctx, `SELECT COUNT(1) FROM topics_history WHERE url_hash = ?`, urlHash).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("check url hash: %w", err)
	}
	return count > 0, nil
}

func (r *Repository) SaveTopic(ctx context.Context, t Topic) (int64, error) {
	if t.Keywords == "" {
		t.Keywords = "[]"
	}
	res, err := r.db.ExecContext(ctx, `
		INSERT INTO topics_history (source_type, source_url, url_hash, title, keywords, summary)
		VALUES (?, ?, ?, ?, ?, ?)
	`, t.SourceType, t.SourceURL, t.URLHash, t.Title, t.Keywords, t.Summary)
	if err != nil {
		return 0, fmt.Errorf("save topic: %w", err)
	}
	return res.LastInsertId()
}

// --- Posts ---

type Post struct {
	ID                   string
	TopicID              *int64
	WeeklyThemeID        *int64
	PatternSlug          *string
	Status               string
	AttemptCount         int
	RequiresHumanReview  bool
	UncertaintyReason    string
	LastReminderAt       *time.Time
	ContentText          string
	MediaType            string
	MediaPayload         string
	MediaLanguage        string
	MediaPath            string
	PublishToLinkedIn    bool
	PublishToSidequest   bool
	SidequestSlug        string
	SidequestMDXPath     string
	TelegramPreviewMsgID *int64
	LinkedInPostURN      string
	ErrorMessage         string
	CreatedAt            time.Time
	UpdatedAt            time.Time
	PublishedAt          *time.Time
}

func (r *Repository) CreatePost(ctx context.Context, p Post) error {
	reqReview, pubLI, pubSQ := 0, 0, 0
	if p.RequiresHumanReview {
		reqReview = 1
	}
	if p.PublishToLinkedIn {
		pubLI = 1
	}
	if p.PublishToSidequest {
		pubSQ = 1
	}
	if p.MediaType == "" {
		p.MediaType = "none"
	}
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO posts (
			id, topic_id, weekly_theme_id, pattern_slug, status, attempt_count,
			requires_human_review, uncertainty_reason, content_text,
			media_type, media_payload, media_language, media_path,
			publish_to_linkedin, publish_to_sidequest, sidequest_slug, sidequest_mdx_path
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		p.ID, p.TopicID, p.WeeklyThemeID, p.PatternSlug, p.Status, p.AttemptCount,
		reqReview, p.UncertaintyReason, p.ContentText,
		p.MediaType, p.MediaPayload, p.MediaLanguage, p.MediaPath,
		pubLI, pubSQ, p.SidequestSlug, p.SidequestMDXPath,
	)
	if err != nil {
		return fmt.Errorf("create post %s: %w", p.ID, err)
	}
	return nil
}

func (r *Repository) GetPostByID(ctx context.Context, id string) (*Post, error) {
	var p Post
	var reqReview, pubLI, pubSQ int
	err := r.db.QueryRowContext(ctx, `
		SELECT id, topic_id, weekly_theme_id, pattern_slug, status, attempt_count,
		       requires_human_review, uncertainty_reason, last_reminder_at, content_text,
		       media_type, media_payload, media_language, media_path,
		       publish_to_linkedin, publish_to_sidequest, sidequest_slug, sidequest_mdx_path,
		       telegram_preview_msg_id, linkedin_post_urn, error_message,
		       created_at, updated_at, published_at
		FROM posts WHERE id = ?
	`, id).Scan(
		&p.ID, &p.TopicID, &p.WeeklyThemeID, &p.PatternSlug, &p.Status, &p.AttemptCount,
		&reqReview, &p.UncertaintyReason, &p.LastReminderAt, &p.ContentText,
		&p.MediaType, &p.MediaPayload, &p.MediaLanguage, &p.MediaPath,
		&pubLI, &pubSQ, &p.SidequestSlug, &p.SidequestMDXPath,
		&p.TelegramPreviewMsgID, &p.LinkedInPostURN, &p.ErrorMessage,
		&p.CreatedAt, &p.UpdatedAt, &p.PublishedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get post %s: %w", id, err)
	}
	p.RequiresHumanReview = reqReview == 1
	p.PublishToLinkedIn = pubLI == 1
	p.PublishToSidequest = pubSQ == 1
	return &p, nil
}

func (r *Repository) UpdatePostStatus(ctx context.Context, id, status, errMsg string) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE posts SET status = ?, error_message = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?
	`, status, errMsg, id)
	if err != nil {
		return fmt.Errorf("update post status %s: %w", id, err)
	}
	return nil
}

func (r *Repository) MarkPublished(ctx context.Context, id, linkedinURN, sidequestMDXPath string) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE posts
		SET status = 'PUBLISHED',
		    linkedin_post_urn = CASE WHEN ? != '' THEN ? ELSE linkedin_post_urn END,
		    sidequest_mdx_path = CASE WHEN ? != '' THEN ? ELSE sidequest_mdx_path END,
		    published_at = CURRENT_TIMESTAMP,
		    updated_at = CURRENT_TIMESTAMP
		WHERE id = ?
	`, linkedinURN, linkedinURN, sidequestMDXPath, sidequestMDXPath, id)
	if err != nil {
		return fmt.Errorf("mark published %s: %w", id, err)
	}
	return nil
}

func (r *Repository) ListPendingApprovalPosts(ctx context.Context) ([]Post, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, topic_id, weekly_theme_id, pattern_slug, status, attempt_count,
		       requires_human_review, uncertainty_reason, last_reminder_at, content_text,
		       media_type, media_payload, media_language, media_path,
		       publish_to_linkedin, publish_to_sidequest, sidequest_slug, sidequest_mdx_path,
		       telegram_preview_msg_id, linkedin_post_urn, error_message,
		       created_at, updated_at, published_at
		FROM posts
		WHERE status = 'AWAITING_APPROVAL'
		ORDER BY created_at ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("list pending approval posts: %w", err)
	}
	defer rows.Close()

	var out []Post
	for rows.Next() {
		var p Post
		var reqReview, pubLI, pubSQ int
		if err := rows.Scan(
			&p.ID, &p.TopicID, &p.WeeklyThemeID, &p.PatternSlug, &p.Status, &p.AttemptCount,
			&reqReview, &p.UncertaintyReason, &p.LastReminderAt, &p.ContentText,
			&p.MediaType, &p.MediaPayload, &p.MediaLanguage, &p.MediaPath,
			&pubLI, &pubSQ, &p.SidequestSlug, &p.SidequestMDXPath,
			&p.TelegramPreviewMsgID, &p.LinkedInPostURN, &p.ErrorMessage,
			&p.CreatedAt, &p.UpdatedAt, &p.PublishedAt,
		); err != nil {
			return nil, fmt.Errorf("scan pending post: %w", err)
		}
		p.RequiresHumanReview = reqReview == 1
		p.PublishToLinkedIn = pubLI == 1
		p.PublishToSidequest = pubSQ == 1
		out = append(out, p)
	}
	return out, rows.Err()
}

func (r *Repository) TouchPostReminder(ctx context.Context, id string) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE posts SET last_reminder_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP WHERE id = ?
	`, id)
	return err
}

func (r *Repository) UpdatePostDraftResult(ctx context.Context, p Post) error {
	reqReview := 0
	if p.RequiresHumanReview {
		reqReview = 1
	}
	_, err := r.db.ExecContext(ctx, `
		UPDATE posts
		SET pattern_slug = ?,
		    status = ?,
		    attempt_count = ?,
		    requires_human_review = ?,
		    uncertainty_reason = ?,
		    content_text = ?,
		    media_type = ?,
		    media_payload = ?,
		    media_language = ?,
		    media_path = ?,
		    sidequest_slug = ?,
		    updated_at = CURRENT_TIMESTAMP
		WHERE id = ?
	`,
		p.PatternSlug, p.Status, p.AttemptCount, reqReview, p.UncertaintyReason,
		p.ContentText, p.MediaType, p.MediaPayload, p.MediaLanguage, p.MediaPath,
		p.SidequestSlug, p.ID,
	)
	if err != nil {
		return fmt.Errorf("update post draft %s: %w", p.ID, err)
	}
	return nil
}

func (r *Repository) UpdatePostPreviewMsgID(ctx context.Context, id string, msgID int) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE posts SET telegram_preview_msg_id = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?
	`, msgID, id)
	return err
}

func (r *Repository) UpdatePostContentText(ctx context.Context, id, text string) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE posts SET content_text = ?, status = 'AWAITING_APPROVAL', updated_at = CURRENT_TIMESTAMP WHERE id = ?
	`, text, id)
	return err
}

func (r *Repository) GetLatestEditingPost(ctx context.Context) (*Post, error) {
	var id string
	err := r.db.QueryRowContext(ctx, `
		SELECT id FROM posts WHERE status = 'EDITING' ORDER BY updated_at DESC LIMIT 1
	`).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return r.GetPostByID(ctx, id)
}

// --- QA Logs ---

type QALog struct {
	ID            int64
	PostID        string
	AttemptNumber int
	DraftSnapshot string
	QAApproved    bool
	QAResponse    string
	CreatedAt     time.Time
}

func (r *Repository) InsertQALog(ctx context.Context, log QALog) error {
	approved := 0
	if log.QAApproved {
		approved = 1
	}
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO qa_logs (post_id, attempt_number, draft_snapshot, qa_approved, qa_response)
		VALUES (?, ?, ?, ?, ?)
	`, log.PostID, log.AttemptNumber, log.DraftSnapshot, approved, log.QAResponse)
	if err != nil {
		return fmt.Errorf("insert qa log: %w", err)
	}
	return nil
}

// --- OAuth Tokens ---

type OAuthToken struct {
	Provider        string
	AccessToken     string
	RefreshToken    string
	PersonURN       string
	Scopes          string
	ExpiresAt       time.Time
	LastAlertSentAt *time.Time
	UpdatedAt       time.Time
}

func (r *Repository) UpsertOAuthToken(ctx context.Context, tok OAuthToken) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO oauth_tokens (provider, access_token, refresh_token, person_urn, scopes, expires_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(provider) DO UPDATE SET
			access_token = excluded.access_token,
			refresh_token = excluded.refresh_token,
			person_urn = excluded.person_urn,
			scopes = excluded.scopes,
			expires_at = excluded.expires_at,
			last_alert_sent_at = NULL,
			updated_at = CURRENT_TIMESTAMP
	`, tok.Provider, tok.AccessToken, tok.RefreshToken, tok.PersonURN, tok.Scopes, tok.ExpiresAt)
	if err != nil {
		return fmt.Errorf("upsert oauth token %s: %w", tok.Provider, err)
	}
	return nil
}

func (r *Repository) GetOAuthToken(ctx context.Context, provider string) (*OAuthToken, error) {
	var t OAuthToken
	err := r.db.QueryRowContext(ctx, `
		SELECT provider, access_token, refresh_token, person_urn, scopes, expires_at, last_alert_sent_at, updated_at
		FROM oauth_tokens WHERE provider = ?
	`, provider).Scan(
		&t.Provider, &t.AccessToken, &t.RefreshToken, &t.PersonURN,
		&t.Scopes, &t.ExpiresAt, &t.LastAlertSentAt, &t.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get oauth token %s: %w", provider, err)
	}
	return &t, nil
}

func (r *Repository) TouchOAuthTokenAlert(ctx context.Context, provider string) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE oauth_tokens SET last_alert_sent_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP WHERE provider = ?
	`, provider)
	return err
}

// --- Post Metrics & Analytics ---

type PostMetric struct {
	ID          int64
	PostID      string
	Impressions int
	Reactions   int
	Comments    int
	Reposts     int
	RawNotes    string
	CapturedAt  time.Time
}

func (r *Repository) InsertPostMetric(ctx context.Context, m PostMetric) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO post_metrics (post_id, impressions, reactions, comments, reposts, raw_notes)
		VALUES (?, ?, ?, ?, ?, ?)
	`, m.PostID, m.Impressions, m.Reactions, m.Comments, m.Reposts, m.RawNotes)
	if err != nil {
		return fmt.Errorf("insert post metric: %w", err)
	}
	return nil
}

func (r *Repository) TouchMetricsReminder(ctx context.Context, postID string) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE posts SET last_metrics_reminder_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP WHERE id = ?
	`, postID)
	return err
}

func (r *Repository) GetLatestPublishedPost(ctx context.Context) (*Post, error) {
	var id string
	err := r.db.QueryRowContext(ctx, `
		SELECT id FROM posts
		WHERE status = 'PUBLISHED' AND linkedin_post_urn != ''
		ORDER BY published_at DESC LIMIT 1
	`).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return r.GetPostByID(ctx, id)
}

// ListPostsNeedingMetricsReminder returns published LinkedIn posts older than intervalDays
// that have no metrics captured since (now - intervalDays) and were not reminded within intervalDays.
func (r *Repository) ListPostsNeedingMetricsReminder(ctx context.Context, intervalDays int) ([]Post, error) {
	if intervalDays <= 0 {
		intervalDays = 3
	}
	offset := fmt.Sprintf("-%d days", intervalDays)
	rows, err := r.db.QueryContext(ctx, `
		SELECT p.id
		FROM posts p
		WHERE p.status = 'PUBLISHED'
		  AND p.linkedin_post_urn != ''
		  AND p.linkedin_post_urn NOT LIKE 'urn:li:dryrun:%'
		  AND p.published_at IS NOT NULL
		  AND p.published_at <= datetime('now', ?)
		  AND (p.last_metrics_reminder_at IS NULL OR p.last_metrics_reminder_at <= datetime('now', ?))
		  AND NOT EXISTS (
		      SELECT 1 FROM post_metrics m
		      WHERE m.post_id = p.id AND m.captured_at >= datetime('now', ?)
		  )
		ORDER BY p.published_at DESC
		LIMIT 5
	`, offset, offset, offset)
	if err != nil {
		return nil, fmt.Errorf("list posts needing metrics reminder: %w", err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	var out []Post
	for _, id := range ids {
		p, err := r.GetPostByID(ctx, id)
		if err == nil {
			out = append(out, *p)
		}
	}
	return out, nil
}

type AnalyticsSummary struct {
	TotalPublished int
	TotalQALogs    int
	FirstTryPasses int
	ByPattern      []PatternStats
	ByMediaType    []MediaStats
}

type PatternStats struct {
	PatternSlug    string
	PostCount      int
	AvgImpressions float64
	AvgReactions   float64
	AvgComments    float64
}

type MediaStats struct {
	MediaType      string
	PostCount      int
	AvgImpressions float64
	AvgReactions   float64
}

func (r *Repository) GetAnalyticsSummary(ctx context.Context) (*AnalyticsSummary, error) {
	var sum AnalyticsSummary
	_ = r.db.QueryRowContext(ctx, `SELECT COUNT(1) FROM posts WHERE status = 'PUBLISHED'`).Scan(&sum.TotalPublished)
	_ = r.db.QueryRowContext(ctx, `SELECT COUNT(1) FROM qa_logs`).Scan(&sum.TotalQALogs)
	_ = r.db.QueryRowContext(ctx, `SELECT COUNT(1) FROM qa_logs WHERE attempt_number = 1 AND qa_approved = 1`).Scan(&sum.FirstTryPasses)

	pRows, err := r.db.QueryContext(ctx, `
		SELECT COALESCE(p.pattern_slug, 'uncategorized'),
		       COUNT(DISTINCT p.id),
		       COALESCE(AVG(m.impressions), 0),
		       COALESCE(AVG(m.reactions), 0),
		       COALESCE(AVG(m.comments), 0)
		FROM posts p
		LEFT JOIN (
			SELECT post_id, MAX(impressions) AS impressions, MAX(reactions) AS reactions, MAX(comments) AS comments
			FROM post_metrics GROUP BY post_id
		) m ON m.post_id = p.id
		WHERE p.status = 'PUBLISHED'
		GROUP BY COALESCE(p.pattern_slug, 'uncategorized')
		ORDER BY AVG(m.impressions) DESC, COUNT(DISTINCT p.id) DESC
		LIMIT 10
	`)
	if err == nil {
		defer pRows.Close()
		for pRows.Next() {
			var ps PatternStats
			if err := pRows.Scan(&ps.PatternSlug, &ps.PostCount, &ps.AvgImpressions, &ps.AvgReactions, &ps.AvgComments); err == nil {
				sum.ByPattern = append(sum.ByPattern, ps)
			}
		}
	}

	mRows, err := r.db.QueryContext(ctx, `
		SELECT p.media_type,
		       COUNT(DISTINCT p.id),
		       COALESCE(AVG(m.impressions), 0),
		       COALESCE(AVG(m.reactions), 0)
		FROM posts p
		LEFT JOIN (
			SELECT post_id, MAX(impressions) AS impressions, MAX(reactions) AS reactions
			FROM post_metrics GROUP BY post_id
		) m ON m.post_id = p.id
		WHERE p.status = 'PUBLISHED'
		GROUP BY p.media_type
		ORDER BY AVG(m.impressions) DESC
	`)
	if err == nil {
		defer mRows.Close()
		for mRows.Next() {
			var ms MediaStats
			if err := mRows.Scan(&ms.MediaType, &ms.PostCount, &ms.AvgImpressions, &ms.AvgReactions); err == nil {
				sum.ByMediaType = append(sum.ByMediaType, ms)
			}
		}
	}

	return &sum, nil
}
