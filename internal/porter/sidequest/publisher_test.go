package sidequest_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"codeberg.org/matheus/porter/internal/agents"
	"codeberg.org/matheus/porter/internal/porter/sidequest"
	"codeberg.org/matheus/porter/internal/settings"
	"codeberg.org/matheus/porter/internal/store"
)

func TestPublishArticleMDXAndCover(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "sq.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	repo := store.NewRepository(db)
	mgr := settings.NewManager(repo)
	_ = mgr.SeedDefaults(ctx)

	fakeRepo := t.TempDir()
	_ = mgr.Set(ctx, settings.KeySidequestRepoPath, fakeRepo)
	_ = mgr.Set(ctx, settings.KeySidequestDraft, "true")

	coverPath := filepath.Join(t.TempDir(), "cover.png")
	_ = os.WriteFile(coverPath, []byte("png"), 0o644)

	pub := sidequest.NewPublisher(mgr)
	relPath, err := pub.PublishArticle(ctx, &agents.SidequestArticle{
		Slug:         "sqlite-wal-go",
		Title:        "SQLite WAL Mode in Go",
		Description:  "How single-writer WAL mode simplifies local-first Go daemons.",
		Type:         "article",
		Area:         "backend",
		Tags:         []string{"go", "sqlite"},
		Stack:        []string{"Go", "SQLite"},
		CoverAlt:     "SQLite WAL snippet",
		MarkdownBody: "## Why WAL\n\n" + strings.Repeat("WAL mode allows concurrent readers. ", 10),
	}, coverPath)
	if err != nil {
		t.Fatalf("PublishArticle: %v", err)
	}
	if relPath != "src/content/blog/en/sqlite-wal-go.mdx" {
		t.Fatalf("unexpected relPath: %q", relPath)
	}

	mdxBytes, err := os.ReadFile(filepath.Join(fakeRepo, relPath))
	if err != nil {
		t.Fatalf("read mdx: %v", err)
	}
	if !strings.Contains(string(mdxBytes), "cover: ./sqlite-wal-go-cover.png") ||
		!strings.Contains(string(mdxBytes), "draft: true") {
		t.Fatalf("unexpected MDX content:\n%s", string(mdxBytes))
	}
}
