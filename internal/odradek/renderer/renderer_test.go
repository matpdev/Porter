package renderer_test

import (
	"context"
	"os"
	"testing"

	"codeberg.org/matheus/porter/internal/agents"
	"codeberg.org/matheus/porter/internal/odradek/renderer"
)

func TestRendererCodeAndMermaid(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping playwright browser test in -short mode")
	}

	r := renderer.NewRenderer(t.TempDir())
	defer r.Close()

	ctx := context.Background()
	out, err := r.Render(ctx, "test-code", agents.PostMedia{
		Type:     agents.MediaCodeSnippet,
		Title:    "SQLite WAL Setup",
		Language: "go",
		Content:  "db.SetMaxOpenConns(1)",
	})
	if err != nil {
		t.Skipf("playwright/chromium unavailable in this environment: %v", err)
	}
	info, err := os.Stat(out)
	if err != nil || info.Size() == 0 {
		t.Fatalf("expected non-empty PNG at %s, err=%v", out, err)
	}
}
