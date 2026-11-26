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
	_ = mgr.Set(ctx, settings.KeySidequestDraft, "false")
	_ = mgr.Set(ctx, settings.KeySidequestAutoCommit, "false")

	coverPath := filepath.Join(t.TempDir(), "cover.png")
	_ = os.WriteFile(coverPath, []byte("png"), 0o644)

	pub := sidequest.NewPublisher(mgr)
	relPaths, err := pub.PublishArticle(ctx, &agents.SidequestArticle{
		Slug:           "sqlite-wal-go",
		Title:          "SQLite WAL Mode in Go",
		Description:    "How single-writer WAL mode simplifies local-first Go daemons.",
		TitlePT:        "SQLite em modo WAL com Go",
		DescriptionPT:  "Como o modo WAL simplifica daemons local-first em Go.",
		Type:           "acerto",
		Area:           "backend",
		Tags:           []string{"go", "sqlite"},
		Stack:          []string{"Go", "SQLite"},
		CoverAlt:       "SQLite WAL snippet",
		CoverAltPT:     "Snippet SQLite WAL",
		MarkdownBody:   "## Why WAL\n\n" + strings.Repeat("WAL mode allows concurrent readers. ", 10),
		MarkdownBodyPT: "## Por que WAL\n\n" + strings.Repeat("O modo WAL permite leituras concorrentes. ", 10),
	}, coverPath)
	if err != nil {
		t.Fatalf("PublishArticle: %v", err)
	}
	if !strings.Contains(relPaths, "en/sqlite-wal-go.mdx") || !strings.Contains(relPaths, "pt/sqlite-wal-go.mdx") {
		t.Fatalf("unexpected relPaths: %q", relPaths)
	}

	enBytes, err := os.ReadFile(filepath.Join(fakeRepo, "src/content/blog/en/sqlite-wal-go.mdx"))
	if err != nil || !strings.Contains(string(enBytes), "cover: ./sqlite-wal-go-cover.png") || !strings.Contains(string(enBytes), "draft: false") {
		t.Fatalf("unexpected EN MDX: err=%v\n%s", err, string(enBytes))
	}
	ptBytes, err := os.ReadFile(filepath.Join(fakeRepo, "src/content/blog/pt/sqlite-wal-go.mdx"))
	if err != nil || !strings.Contains(string(ptBytes), "SQLite em modo WAL com Go") {
		t.Fatalf("unexpected PT MDX: err=%v\n%s", err, string(ptBytes))
	}
}
