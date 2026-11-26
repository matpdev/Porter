package linkedin_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"codeberg.org/matheus/porter/internal/porter/linkedin"
	"codeberg.org/matheus/porter/internal/settings"
	"codeberg.org/matheus/porter/internal/store"
)

func setupOAuthDeps(t *testing.T) (*store.Repository, *settings.Manager) {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "oauth.db"))
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

func TestExchangeCodeAndPersistToken(t *testing.T) {
	ctx := context.Background()
	repo, mgr := setupOAuthDeps(t)

	_ = mgr.Set(ctx, settings.KeyLinkedInClientID, "cid-123")
	_ = mgr.Set(ctx, settings.KeyLinkedInClientSecret, "csec-456")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth/v2/accessToken":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "tok-abc",
				"expires_in":   5184000,
				"scope":        "openid profile w_member_social",
			})
		case "/v2/userinfo":
			if r.Header.Get("Authorization") != "Bearer tok-abc" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"sub":  "user-999",
				"name": "Matheus",
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	svc := linkedin.NewOAuthService(repo, mgr, srv.Client()).WithEndpoints(linkedin.Endpoints{
		AuthorizeURL: srv.URL + "/oauth/v2/authorization",
		TokenURL:     srv.URL + "/oauth/v2/accessToken",
		UserInfoURL:  srv.URL + "/v2/userinfo",
	})

	tok, err := svc.ExchangeCode(ctx, "auth-code-1")
	if err != nil {
		t.Fatalf("exchange code: %v", err)
	}
	if tok.PersonURN != "urn:li:person:user-999" || tok.AccessToken != "tok-abc" {
		t.Fatalf("unexpected token: %+v", tok)
	}

	stored, err := repo.GetOAuthToken(ctx, linkedin.ProviderLinkedIn)
	if err != nil || stored.PersonURN != "urn:li:person:user-999" {
		t.Fatalf("expected persisted token, got %+v, err=%v", stored, err)
	}
}

func TestEvaluateTokenAlertThresholds(t *testing.T) {
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)

	ok, _ := linkedin.EvaluateTokenAlert(&store.OAuthToken{ExpiresAt: now.Add(30 * 24 * time.Hour)}, now)
	if ok {
		t.Fatalf("did not expect alert at 30 days")
	}

	ok, msg := linkedin.EvaluateTokenAlert(&store.OAuthToken{ExpiresAt: now.Add(6 * 24 * time.Hour)}, now)
	if !ok || msg == "" {
		t.Fatalf("expected alert at 6 days")
	}

	recent := now.Add(-2 * time.Hour)
	ok, _ = linkedin.EvaluateTokenAlert(&store.OAuthToken{
		ExpiresAt:       now.Add(6 * 24 * time.Hour),
		LastAlertSentAt: &recent,
	}, now)
	if ok {
		t.Fatalf("expected alert to be throttled within 24h")
	}

	ok, msg = linkedin.EvaluateTokenAlert(&store.OAuthToken{ExpiresAt: now.Add(-1 * time.Hour)}, now)
	if !ok || msg == "" {
		t.Fatalf("expected alert when expired")
	}
}
