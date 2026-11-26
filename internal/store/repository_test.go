package store_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"codeberg.org/matheus/porter/internal/store"
)

func openTestRepo(t *testing.T) *store.Repository {
	t.Helper()
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return store.NewRepository(db)
}

func TestSettingsAndPatterns(t *testing.T) {
	ctx := context.Background()
	repo := openTestRepo(t)

	if err := repo.EnsureSettingDefault(ctx, "pipeline.dry_run", "false", false); err != nil {
		t.Fatalf("ensure default: %v", err)
	}
	if err := repo.SetSetting(ctx, "ai.deepseek_api_key", "sk-secret", true); err != nil {
		t.Fatalf("set secret: %v", err)
	}

	s, err := repo.GetSetting(ctx, "ai.deepseek_api_key")
	if err != nil || !s.IsSecret || s.Value != "sk-secret" {
		t.Fatalf("unexpected setting: %+v, err=%v", s, err)
	}

	p, err := repo.GetOrCreatePattern(ctx, "go-concurrency", "Go Concurrency")
	if err != nil {
		t.Fatalf("create pattern: %v", err)
	}
	if p.AutoPublish {
		t.Fatalf("new pattern must not auto-publish by default")
	}

	p, err = repo.IncrementPatternApproval(ctx, "go-concurrency", 2)
	if err != nil || p.AutoPublish || p.ApprovedCount != 1 {
		t.Fatalf("expected 1 approval and auto_publish=false, got %+v, err=%v", p, err)
	}

	p, err = repo.IncrementPatternApproval(ctx, "go-concurrency", 2)
	if err != nil || !p.AutoPublish || p.ApprovedCount != 2 {
		t.Fatalf("expected 2 approvals and auto_publish=true, got %+v, err=%v", p, err)
	}
}

func TestWeeklyThemesAndPostsLifecycle(t *testing.T) {
	ctx := context.Background()
	repo := openTestRepo(t)

	id, err := repo.CreateWeeklyTheme(ctx, store.WeeklyTheme{
		WeekStart:       "2026-11-23",
		ScheduledDay:    "tuesday",
		Title:           "SQLite WAL Mode in Go",
		Angle:           "Why single-writer WAL beats Postgres for local daemons",
		TargetLinkedIn:  true,
		TargetSidequest: true,
	})
	if err != nil || id == 0 {
		t.Fatalf("create weekly theme: %v", err)
	}

	next, err := repo.GetNextPlannedTheme(ctx, "2026-11-25")
	if err != nil {
		t.Fatalf("get next planned theme: %v", err)
	}
	if next.Title != "SQLite WAL Mode in Go" || !next.TargetSidequest {
		t.Fatalf("unexpected theme: %+v", next)
	}

	hash := "abc123"
	topicID, err := repo.SaveTopic(ctx, store.Topic{
		SourceType: "weekly_theme",
		URLHash:    &hash,
		Title:      next.Title,
	})
	if err != nil {
		t.Fatalf("save topic: %v", err)
	}
	processed, err := repo.IsURLProcessed(ctx, hash)
	if err != nil || !processed {
		t.Fatalf("expected processed=true, err=%v", err)
	}

	postID := "post-uuid-1"
	if err := repo.CreatePost(ctx, store.Post{
		ID:                  postID,
		TopicID:             &topicID,
		WeeklyThemeID:       &next.ID,
		Status:              "AWAITING_APPROVAL",
		RequiresHumanReview: true,
		UncertaintyReason:   "New topic pattern: sqlite-internals",
		PublishToLinkedIn:   true,
		PublishToSidequest:  true,
	}); err != nil {
		t.Fatalf("create post: %v", err)
	}

	pending, err := repo.ListPendingApprovalPosts(ctx)
	if err != nil || len(pending) != 1 || pending[0].ID != postID {
		t.Fatalf("unexpected pending posts: %+v, err=%v", pending, err)
	}

	if err := repo.InsertQALog(ctx, store.QALog{
		PostID:        postID,
		AttemptNumber: 1,
		DraftSnapshot: `{"hook":"test"}`,
		QAApproved:    true,
		QAResponse:    `{"approved":true}`,
	}); err != nil {
		t.Fatalf("insert qa log: %v", err)
	}

	if err := repo.MarkPublished(ctx, postID, "urn:li:share:999", "src/content/blog/en/sqlite-wal.mdx"); err != nil {
		t.Fatalf("mark published: %v", err)
	}

	got, err := repo.GetPostByID(ctx, postID)
	if err != nil || got.Status != "PUBLISHED" || got.LinkedInPostURN != "urn:li:share:999" {
		t.Fatalf("unexpected post after publish: %+v, err=%v", got, err)
	}

	if err := repo.UpsertOAuthToken(ctx, store.OAuthToken{
		Provider:    "linkedin",
		AccessToken: "tok",
		PersonURN:   "urn:li:person:123",
		Scopes:      "openid profile w_member_social",
		ExpiresAt:   time.Now().Add(60 * 24 * time.Hour),
	}); err != nil {
		t.Fatalf("upsert token: %v", err)
	}
	tok, err := repo.GetOAuthToken(ctx, "linkedin")
	if err != nil || tok.PersonURN != "urn:li:person:123" {
		t.Fatalf("unexpected token: %+v, err=%v", tok, err)
	}
}
