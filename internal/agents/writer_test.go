package agents_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"codeberg.org/matheus/porter/internal/agents"
	"codeberg.org/matheus/porter/internal/odradek/curator"
	"codeberg.org/matheus/porter/internal/settings"
	"codeberg.org/matheus/porter/internal/store"
)

func TestWriterAgentDraftPostWithGuidelinesAndFeedback(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "writer.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	repo := store.NewRepository(db)
	mgr := settings.NewManager(repo)
	_ = mgr.SeedDefaults(ctx)
	_ = mgr.Set(ctx, settings.KeyDeepSeekAPIKey, "sk-test-key")

	var capturedUserPrompt string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		for _, m := range req.Messages {
			if m.Role == "user" {
				capturedUserPrompt = m.Content
			}
		}

		draft := agents.PostDraft{
			TopicPatternSlug:  "sqlite-internals",
			TopicPatternLabel: "SQLite Internals",
			Hook:              "Pinning SQLite to a single connection in Go eliminates SQLITE_BUSY under WAL.",
			Body:              strings.Repeat("In local-first daemons, modernc.org/sqlite with SetMaxOpenConns(1) and WAL mode keeps reads fast and writes serialized cleanly. ", 2),
			CallToAction:      "How do you handle local persistence in your Go daemons?",
			Hashtags:          []string{"#GoLang", "#SQLite"},
			Media: agents.PostMedia{
				Type:     agents.MediaCodeSnippet,
				Title:    "SQLite WAL in Go",
				Language: "go",
				Content:  "db.SetMaxOpenConns(1)",
			},
		}
		payload, _ := json.Marshal(draft)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]any{"role": "assistant", "content": string(payload)}},
			},
		})
	}))
	defer srv.Close()

	writer := agents.NewWriterAgent(mgr).WithBaseURL(srv.URL)
	got, err := writer.DraftPost(ctx, agents.WriterInput{
		Topic: curator.CuratedTopic{
			SourceType: "weekly_theme",
			Title:      "SQLite WAL in Go",
			Summary:    "WAL mode details",
		},
		PatternGuidelines: "Always mention modernc.org/sqlite",
		PreviousFeedback:  "Shorten the code snippet",
		Attempt:           2,
	})
	if err != nil {
		t.Fatalf("DraftPost: %v", err)
	}
	if got.TopicPatternSlug != "sqlite-internals" {
		t.Fatalf("unexpected slug: %q", got.TopicPatternSlug)
	}
	if !strings.Contains(capturedUserPrompt, "Always mention modernc.org/sqlite") ||
		!strings.Contains(capturedUserPrompt, "Shorten the code snippet") {
		t.Fatalf("expected guidelines and QA feedback in prompt, got: %s", capturedUserPrompt)
	}
}
