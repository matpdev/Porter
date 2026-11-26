package curator_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"codeberg.org/matheus/porter/internal/odradek/curator"
	"codeberg.org/matheus/porter/internal/settings"
	"codeberg.org/matheus/porter/internal/store"
)

func setupCuratorDB(t *testing.T) (*store.Repository, *settings.Manager) {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "curator.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	repo := store.NewRepository(db)
	mgr := settings.NewManager(repo)
	if err := mgr.SeedDefaults(ctx); err != nil {
		t.Fatalf("seed defaults: %v", err)
	}
	return repo, mgr
}

func TestNormalizeAndHashURL(t *testing.T) {
	a, err := curator.NormalizeURL("HTTPS://Example.COM/posts/go-wal/?utm_source=hn&id=42#top")
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	b, err := curator.NormalizeURL("https://example.com/posts/go-wal?id=42")
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if a != b || curator.HashURL(a) != curator.HashURL(b) {
		t.Fatalf("expected identical normalized URLs, got %q vs %q", a, b)
	}
}

func TestExtractorAndFeedsWithDeduplication(t *testing.T) {
	ctx := context.Background()
	repo, mgr := setupCuratorDB(t)

	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/topstories.json":
			_ = json.NewEncoder(w).Encode([]int64{101})
		case "/item/101.json":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":    101,
				"title": "SQLite WAL Internals",
				"url":   srv.URL + "/article?utm_source=hn",
				"score": 150,
			})
		case "/article":
			w.Header().Set("Content-Type", "text/html")
			_, _ = fmt.Fprint(w, `<html><head><title>SQLite WAL Internals</title></head><body><article><h1>SQLite WAL Internals</h1><p>Write-Ahead Logging allows concurrent readers and a single writer without blocking readers.</p></article></body></html>`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	_ = mgr.Set(ctx, settings.KeyCuratorRSSFeeds, "")
	feeds := curator.NewFeedCollector(srv.Client()).WithHNBaseURL(srv.URL)
	ext := curator.NewExtractor(srv.Client())
	svc := curator.NewService(repo, mgr, ext, feeds, nil, nil)

	topic, err := svc.CurateFromFeeds(ctx)
	if err != nil {
		t.Fatalf("curate from feeds: %v", err)
	}
	if !strings.Contains(topic.Summary, "Write-Ahead Logging") {
		t.Fatalf("expected extracted article text, got %q", topic.Summary)
	}

	// Second call must skip the already-processed URL and return ErrNoFreshTopics.
	if _, err := svc.CurateFromFeeds(ctx); err != curator.ErrNoFreshTopics {
		t.Fatalf("expected ErrNoFreshTopics on duplicate, got %v", err)
	}
}

func TestGitHubAndCodebergWeeklyActivity(t *testing.T) {
	ctx := context.Background()
	recent := time.Now().UTC().Add(-24 * time.Hour).Format(time.RFC3339)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/gh/users/matheus/events/public":
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{
					"type":       "PushEvent",
					"created_at": recent,
					"repo":       map[string]string{"name": "matheus/sidequestlab.dev"},
					"payload": map[string]any{
						"commits": []map[string]string{{"message": "feat: add dark mode toggle"}},
					},
				},
			})
		case "/cb/users/matheus/activities/feeds":
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{
					"op_type": "commit_repo",
					"created": recent,
					"repo":    map[string]string{"full_name": "matheus/porter"},
					"content": `{"Commits":[{"Message":"feat: implement odradek curator"}]}`,
				},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	collector := curator.NewGitActivityCollector(srv.Client()).WithBaseURLs(srv.URL+"/gh", srv.URL+"/cb")
	updates, err := collector.CollectWeeklySummary(ctx, curator.GitSummaryConfig{
		GitHubUsername:   "matheus",
		CodebergUsername: "matheus",
	})
	if err != nil {
		t.Fatalf("collect weekly summary: %v", err)
	}
	formatted := curator.FormatRepoUpdates(updates)
	if !strings.Contains(formatted, "sidequestlab.dev") || !strings.Contains(formatted, "matheus/porter") {
		t.Fatalf("unexpected formatted git summary: %s", formatted)
	}
}

func TestCurateFromSchedulePrioritizesWeeklyTheme(t *testing.T) {
	ctx := context.Background()
	repo, mgr := setupCuratorDB(t)

	_, err := repo.CreateWeeklyTheme(ctx, store.WeeklyTheme{
		WeekStart:       "2020-01-01",
		Title:           "Planned Theme First",
		Angle:           "Focus on tradeoffs",
		TargetLinkedIn:  true,
		TargetSidequest: true,
	})
	if err != nil {
		t.Fatalf("create theme: %v", err)
	}

	svc := curator.NewService(repo, mgr, nil, nil, nil, nil)
	topic, err := svc.CurateFromSchedule(ctx)
	if err != nil {
		t.Fatalf("curate from schedule: %v", err)
	}
	if topic.SourceType != "weekly_theme" || topic.Title != "Planned Theme First" || !topic.TargetSidequest {
		t.Fatalf("unexpected topic: %+v", topic)
	}
}

func TestParseRepoIntentAndInspectBranch(t *testing.T) {
	req, ok := curator.ParseRepoIntent("Verifique o meu matpdev/zed no github com a branch custom e veja o que criei")
	if !ok || req.Forge != "github" || req.FullName() != "matpdev/zed" || req.Branch != "custom" {
		t.Fatalf("unexpected ParseRepoIntent result: ok=%v req=%+v", ok, req)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/matpdev/zed":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"full_name":      "matpdev/zed",
				"default_branch": "main",
				"fork":           true,
				"html_url":       "https://github.com/matpdev/zed",
				"parent": map[string]any{
					"full_name":      "zed-industries/zed",
					"default_branch": "main",
					"owner":          map[string]string{"login": "zed-industries"},
				},
			})
		case strings.HasPrefix(r.URL.Path, "/repos/matpdev/zed/compare/"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ahead_by": 2,
				"commits": []map[string]any{
					{"commit": map[string]string{"message": "feat: custom bridges theme"}},
				},
				"files": []map[string]any{
					{"filename": "assets/themes/bridges.json", "status": "added", "additions": 120, "deletions": 0},
				},
			})
		case r.URL.Path == "/repos/matpdev/zed/commits":
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"commit": map[string]string{"message": "feat: custom bridges theme"}},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	ctx := context.Background()
	repo, mgr := setupCuratorDB(t)
	svc := curator.NewService(repo, mgr, nil, nil, nil, nil).WithRepoInspector(
		curator.NewRepoInspector(srv.Client()).WithBaseURLs(srv.URL, srv.URL),
	)

	topic, err := svc.CurateFromText(ctx, "Verifique o meu matpdev/zed no github com a branch custom e veja o que criei")
	if err != nil {
		t.Fatalf("CurateFromText: %v", err)
	}
	if !strings.Contains(topic.Summary, "custom bridges theme") || !strings.Contains(topic.Summary, "assets/themes/bridges.json") {
		t.Fatalf("expected repo diff summary, got:\n%s", topic.Summary)
	}
}
