package linkedin_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"codeberg.org/matheus/porter/internal/porter/linkedin"
	"codeberg.org/matheus/porter/internal/settings"
	"codeberg.org/matheus/porter/internal/store"
)

func TestPublishPostWithImageAndDryRun(t *testing.T) {
	ctx := context.Background()
	repo, mgr := setupOAuthDeps(t)

	_ = repo.UpsertOAuthToken(ctx, store.OAuthToken{
		Provider:    linkedin.ProviderLinkedIn,
		AccessToken: "live-token",
		PersonURN:   "urn:li:person:abc",
		Scopes:      linkedin.DefaultScopes,
		ExpiresAt:   time.Now().UTC().Add(30 * 24 * time.Hour),
	})

	pngPath := filepath.Join(t.TempDir(), "card.png")
	_ = os.WriteFile(pngPath, []byte("fake-png-bytes"), 0o644)

	var srv *httptest.Server
	var uploaded, posted bool
	var capturedCommentary string
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/rest/images":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"value": map[string]string{
					"uploadUrl": srv.URL + "/upload-bin",
					"image":     "urn:li:image:img-1",
				},
			})
		case "/upload-bin":
			if r.Method == http.MethodPut {
				uploaded = true
				w.WriteHeader(http.StatusCreated)
			}
		case "/rest/posts":
			posted = true
			var body struct {
				Commentary string `json:"commentary"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			capturedCommentary = body.Commentary
			w.Header().Set("x-restli-id", "urn:li:share:777")
			w.WriteHeader(http.StatusCreated)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	client := linkedin.NewClient(repo, mgr, srv.Client()).WithBaseURL(srv.URL)
	urn, err := client.PublishPost(ctx, "post-1", "Hello LinkedIn (Rust core) #Go", pngPath)
	if err != nil {
		t.Fatalf("PublishPost: %v", err)
	}
	if !uploaded || !posted || urn != "urn:li:share:777" {
		t.Fatalf("unexpected publish state: uploaded=%v posted=%v urn=%q", uploaded, posted, urn)
	}
	if capturedCommentary != `Hello LinkedIn \(Rust core\) #Go` {
		t.Fatalf("expected escaped commentary, got %q", capturedCommentary)
	}

	// Dry run mode must not hit external API.
	_ = mgr.Set(ctx, settings.KeyDryRun, "true")
	dryURN, err := client.PublishPost(ctx, "post-2", "Dry run", "")
	if err != nil || dryURN != "urn:li:dryrun:post-2" {
		t.Fatalf("expected dryrun urn, got %q, err=%v", dryURN, err)
	}
}
