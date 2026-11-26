package pipeline_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"codeberg.org/matheus/porter/internal/agents"
	"codeberg.org/matheus/porter/internal/odradek/curator"
	"codeberg.org/matheus/porter/internal/pipeline"
	"codeberg.org/matheus/porter/internal/settings"
	"codeberg.org/matheus/porter/internal/store"
)

type mockWriter struct {
	attempts int
}

func (m *mockWriter) DraftPost(_ context.Context, in agents.WriterInput) (*agents.PostDraft, error) {
	m.attempts++
	return &agents.PostDraft{
		TopicPatternSlug:  "go-concurrency",
		TopicPatternLabel: "Go Concurrency",
		Hook:              "Structured concurrency in Go keeps goroutine leaks out of production.",
		Body:              strings.Repeat("Using errgroup and context cancellation together guarantees predictable shutdowns. ", 2),
		CallToAction:      "How do you track goroutine lifetimes?",
		Hashtags:          []string{"#GoLang", "#Concurrency"},
		Media:             agents.PostMedia{Type: agents.MediaNone},
	}, nil
}

func (m *mockWriter) DraftSidequestArticle(_ context.Context, _ agents.WriterInput, _ *agents.PostDraft) (*agents.SidequestArticle, error) {
	return &agents.SidequestArticle{
		Slug:           "go-concurrency",
		Title:          "Structured Concurrency in Go",
		Description:    "Preventing goroutine leaks with errgroup and context.",
		TitlePT:        "Concorrência estruturada em Go",
		DescriptionPT:  "Evitando vazamento de goroutines com errgroup e context.",
		Type:           "acerto",
		Area:           "backend",
		Tags:           []string{"go"},
		Stack:          []string{"Go"},
		MarkdownBody:   strings.Repeat("Content. ", 50),
		MarkdownBodyPT: strings.Repeat("Conteúdo. ", 50),
	}, nil
}

type mockQA struct {
	failFirst      bool
	hasUncertainty bool
	calls          int
}

func (m *mockQA) Review(_ context.Context, _ string, _ *agents.PostDraft, _ string) (*agents.QAReview, error) {
	m.calls++
	if m.failFirst && m.calls == 1 {
		return &agents.QAReview{
			Approved:        false,
			TextScore:       5,
			ConfidenceScore: 6,
			Issues:          []agents.QAIssue{{Category: "ai_cliche", Severity: "critical", Description: "Too generic", FixInstruction: "Be specific"}},
			SummaryFeedback: "Be more specific",
		}, nil
	}
	return &agents.QAReview{
		Approved:          true,
		TextScore:         9,
		ConfidenceScore:   9,
		HasUncertainty:    m.hasUncertainty,
		UncertaintyReason: func() string { if m.hasUncertainty { return "Needs human check" }; return "" }(),
		Issues:            []agents.QAIssue{},
		SummaryFeedback:   "Looks solid",
	}, nil
}

type mockLinkedIn struct{ published int }

func (m *mockLinkedIn) PublishPost(_ context.Context, postID, _, _ string) (string, error) {
	m.published++
	return "urn:li:share:" + postID, nil
}

type mockTelegram struct {
	alerts   []string
	previews int
}

func (m *mockTelegram) SendAlert(_ context.Context, text string) error {
	m.alerts = append(m.alerts, text)
	return nil
}

func (m *mockTelegram) SendPreview(_ context.Context, _, _, _, _, _ string) (int, error) {
	m.previews++
	return 42, nil
}

func (m *mockTelegram) DownloadFile(_ context.Context, _ string) ([]byte, error) {
	return nil, nil
}

func setupOrchDB(t *testing.T) (*store.Repository, *settings.Manager) {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "orch.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	repo := store.NewRepository(db)
	mgr := settings.NewManager(repo)
	_ = mgr.SeedDefaults(ctx)
	return repo, mgr
}

func TestOrchestratorNewTopicWaitsAndReminders(t *testing.T) {
	ctx := context.Background()
	repo, mgr := setupOrchDB(t)

	w := &mockWriter{}
	qa := &mockQA{failFirst: true}
	li := &mockLinkedIn{}
	tg := &mockTelegram{}

	orch := pipeline.NewOrchestrator(repo, mgr, nil, w, nil, qa, li, nil, tg, nil)

	err := orch.RunTopic(ctx, curator.CuratedTopic{
		SourceType:     "weekly_theme",
		Title:          "Go Concurrency",
		Summary:        "errgroup patterns",
		TargetLinkedIn: true,
	}, true)
	if err != nil {
		t.Fatalf("RunTopic: %v", err)
	}

	if w.attempts != 2 || qa.calls != 2 {
		t.Fatalf("expected 2 QA loop attempts, got writer=%d qa=%d", w.attempts, qa.calls)
	}
	if li.published != 0 || tg.previews != 1 {
		t.Fatalf("new untrusted topic must wait for approval: published=%d previews=%d", li.published, tg.previews)
	}

	if err := orch.SendPendingReminders(ctx); err != nil {
		t.Fatalf("SendPendingReminders: %v", err)
	}
	if len(tg.alerts) != 1 || !strings.Contains(tg.alerts[0], "Odradek Review Reminder") {
		t.Fatalf("expected reminder alert, got %+v", tg.alerts)
	}
}

func TestOrchestratorTrustedTopicAutoPublishesOnSchedule(t *testing.T) {
	ctx := context.Background()
	repo, mgr := setupOrchDB(t)

	_, _ = repo.GetOrCreatePattern(ctx, "go-concurrency", "Go Concurrency")
	_ = repo.SetPatternTrust(ctx, "go-concurrency", true, "Focus on errgroup")

	w := &mockWriter{}
	qa := &mockQA{}
	li := &mockLinkedIn{}
	tg := &mockTelegram{}

	orch := pipeline.NewOrchestrator(repo, mgr, nil, w, nil, qa, li, nil, tg, nil)

	err := orch.RunTopic(ctx, curator.CuratedTopic{
		SourceType:     "weekly_theme",
		Title:          "Go Concurrency",
		Summary:        "errgroup patterns",
		TargetLinkedIn: true,
	}, true)
	if err != nil {
		t.Fatalf("RunTopic: %v", err)
	}
	if li.published != 1 || tg.previews != 0 {
		t.Fatalf("trusted topic with high confidence should auto-publish: published=%d previews=%d", li.published, tg.previews)
	}
}
