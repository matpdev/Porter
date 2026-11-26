package agents_test

import (
	"encoding/json"
	"strings"
	"testing"

	"codeberg.org/matheus/porter/internal/agents"
)

func TestPostDraftValidationAndFormatting(t *testing.T) {
	valid := &agents.PostDraft{
		TopicPatternSlug:  "sqlite-internals",
		TopicPatternLabel: "SQLite Internals",
		Hook:              "SQLite in WAL mode handles thousands of reads per second with zero network hops.",
		Body:              strings.Repeat("Single-writer concurrency in Go pairs naturally with SQLite WAL mode when you pin MaxOpenConns(1). ", 2),
		CallToAction:      "Are you using SQLite in production for local-first daemons?",
		Hashtags:          []string{"GoLang", "#SQLite", "Software Engineering", "#Dev", "#Backend", "#ExtraTag"},
		Media: agents.PostMedia{
			Type:     agents.MediaCodeSnippet,
			Title:    "SQLite WAL setup in Go",
			Language: "go",
			Content:  "db.SetMaxOpenConns(1)",
		},
	}

	if err := agents.ValidatePostDraft(valid); err != nil {
		t.Fatalf("expected valid PostDraft, got %v", err)
	}

	formatted := valid.FormatForLinkedIn()
	if !strings.Contains(formatted, "#SQLite") || len(formatted) > 3000 {
		t.Fatalf("unexpected formatted output: %q", formatted)
	}

	// Ensure duplicate hook at the start of body is stripped.
	dup := *valid
	dup.Body = dup.Hook + "\n\n" + dup.Body
	if strings.Count(dup.FormatForLinkedIn(), dup.Hook) != 1 {
		t.Fatalf("expected duplicate hook to be stripped, got:\n%s", dup.FormatForLinkedIn())
	}

	invalid := *valid
	invalid.Media.Content = ""
	if err := agents.ValidatePostDraft(&invalid); err == nil {
		t.Fatalf("expected error when media.content is empty for code_snippet")
	}
}

func TestQAReviewValidationAndSchemas(t *testing.T) {
	score := 9
	qa := &agents.QAReview{
		Approved:          true,
		TextScore:         9,
		VisualScore:       &score,
		ConfidenceScore:   9,
		HasUncertainty:    false,
		Issues:            []agents.QAIssue{},
		SummaryFeedback:   "Clean English phrasing and sharp code snippet.",
	}
	if err := agents.ValidateQAReview(qa); err != nil {
		t.Fatalf("expected valid QAReview, got %v", err)
	}

	for name, fn := range map[string]func() ([]byte, error){
		"PostDraft":        agents.PostDraftSchemaJSON,
		"SidequestArticle": agents.SidequestArticleSchemaJSON,
		"QAReview":         agents.QAReviewSchemaJSON,
	} {
		raw, err := fn()
		if err != nil {
			t.Fatalf("%s schema error: %v", name, err)
		}
		var obj map[string]any
		if err := json.Unmarshal(raw, &obj); err != nil {
			t.Fatalf("%s schema invalid JSON: %v", name, err)
		}
	}
}
