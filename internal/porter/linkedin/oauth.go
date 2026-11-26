package linkedin

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"codeberg.org/matheus/porter/internal/settings"
	"codeberg.org/matheus/porter/internal/store"
)

const (
	ProviderLinkedIn = "linkedin"
	DefaultScopes    = "openid profile w_member_social"
)

type Endpoints struct {
	AuthorizeURL string
	TokenURL     string
	UserInfoURL  string
}

func DefaultEndpoints() Endpoints {
	return Endpoints{
		AuthorizeURL: "https://www.linkedin.com/oauth/v2/authorization",
		TokenURL:     "https://www.linkedin.com/oauth/v2/accessToken",
		UserInfoURL:  "https://api.linkedin.com/v2/userinfo",
	}
}

type OAuthService struct {
	repo      *store.Repository
	settings  *settings.Manager
	client    *http.Client
	endpoints Endpoints
}

func NewOAuthService(repo *store.Repository, mgr *settings.Manager, client *http.Client) *OAuthService {
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	return &OAuthService{
		repo:      repo,
		settings:  mgr,
		client:    client,
		endpoints: DefaultEndpoints(),
	}
}

func (s *OAuthService) WithEndpoints(ep Endpoints) *OAuthService {
	s.endpoints = ep
	return s
}

func (s *OAuthService) BuildAuthURL(ctx context.Context) (authURL string, state string, err error) {
	clientID, err := s.settings.GetString(ctx, settings.KeyLinkedInClientID)
	if err != nil || strings.TrimSpace(clientID) == "" {
		return "", "", fmt.Errorf("setting %s is not configured", settings.KeyLinkedInClientID)
	}
	redirectURI, err := s.settings.GetString(ctx, settings.KeyLinkedInRedirectURI)
	if err != nil || strings.TrimSpace(redirectURI) == "" {
		return "", "", fmt.Errorf("setting %s is not configured", settings.KeyLinkedInRedirectURI)
	}

	state, err = randomHex(16)
	if err != nil {
		return "", "", err
	}

	q := url.Values{}
	q.Set("response_type", "code")
	q.Set("client_id", strings.TrimSpace(clientID))
	q.Set("redirect_uri", strings.TrimSpace(redirectURI))
	q.Set("state", state)
	q.Set("scope", DefaultScopes)

	return s.endpoints.AuthorizeURL + "?" + q.Encode(), state, nil
}

// RunCallbackServer starts a temporary HTTP listener on the redirect URI's host:port,
// waits for the OAuth2 callback, exchanges the authorization code, and persists the token.
func (s *OAuthService) RunCallbackServer(ctx context.Context, expectedState string, timeout time.Duration) (*store.OAuthToken, error) {
	redirectURI, err := s.settings.GetString(ctx, settings.KeyLinkedInRedirectURI)
	if err != nil {
		return nil, err
	}
	parsed, err := url.Parse(strings.TrimSpace(redirectURI))
	if err != nil || parsed.Host == "" {
		return nil, fmt.Errorf("invalid %s: %q", settings.KeyLinkedInRedirectURI, redirectURI)
	}
	callbackPath := parsed.Path
	if callbackPath == "" {
		callbackPath = "/"
	}

	ln, err := net.Listen("tcp", parsed.Host)
	if err != nil {
		return nil, fmt.Errorf("listen on %s: %w", parsed.Host, err)
	}
	defer ln.Close()

	type result struct {
		tok *store.OAuthToken
		err error
	}
	ch := make(chan result, 1)

	mux := http.NewServeMux()
	mux.HandleFunc(callbackPath, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if errParam := q.Get("error"); errParam != "" {
			desc := q.Get("error_description")
			http.Error(w, "LinkedIn OAuth error: "+errParam, http.StatusBadRequest)
			ch <- result{err: fmt.Errorf("linkedin oauth error %s: %s", errParam, desc)}
			return
		}
		if q.Get("state") != expectedState {
			http.Error(w, "Invalid OAuth state", http.StatusBadRequest)
			ch <- result{err: errors.New("oauth state mismatch")}
			return
		}
		code := q.Get("code")
		if code == "" {
			http.Error(w, "Missing code", http.StatusBadRequest)
			ch <- result{err: errors.New("missing authorization code")}
			return
		}

		tok, err := s.ExchangeCode(r.Context(), code)
		if err != nil {
			http.Error(w, "Token exchange failed: "+err.Error(), http.StatusInternalServerError)
			ch <- result{err: err}
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, "<h2>Porter LinkedIn OAuth2 connected!</h2><p>You can close this tab and return to Telegram/CLI.</p>")
		ch <- result{tok: tok}
	})

	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()
	defer func() {
		shutCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
	}()

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timer.C:
		return nil, fmt.Errorf("oauth callback timed out after %s", timeout)
	case res := <-ch:
		return res.tok, res.err
	}
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	ExpiresIn    int64  `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
	Scope        string `json:"scope"`
}

type userInfoResponse struct {
	Sub  string `json:"sub"`
	Name string `json:"name"`
}

func (s *OAuthService) ExchangeCode(ctx context.Context, code string) (*store.OAuthToken, error) {
	clientID, err := s.settings.GetString(ctx, settings.KeyLinkedInClientID)
	if err != nil || strings.TrimSpace(clientID) == "" {
		return nil, fmt.Errorf("missing %s", settings.KeyLinkedInClientID)
	}
	clientSecret, err := s.settings.GetString(ctx, settings.KeyLinkedInClientSecret)
	if err != nil || strings.TrimSpace(clientSecret) == "" {
		return nil, fmt.Errorf("missing %s", settings.KeyLinkedInClientSecret)
	}
	redirectURI, err := s.settings.GetString(ctx, settings.KeyLinkedInRedirectURI)
	if err != nil || strings.TrimSpace(redirectURI) == "" {
		return nil, fmt.Errorf("missing %s", settings.KeyLinkedInRedirectURI)
	}

	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", strings.TrimSpace(redirectURI))
	form.Set("client_id", strings.TrimSpace(clientID))
	form.Set("client_secret", strings.TrimSpace(clientSecret))

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoints.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("exchange token request: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("linkedin token endpoint returned %d: %s", resp.StatusCode, string(body))
	}

	var tr tokenResponse
	if err := json.Unmarshal(body, &tr); err != nil {
		return nil, fmt.Errorf("decode token response: %w", err)
	}
	if tr.AccessToken == "" {
		return nil, errors.New("empty access_token in response")
	}

	personURN, err := s.fetchPersonURN(ctx, tr.AccessToken)
	if err != nil {
		return nil, err
	}

	scopes := tr.Scope
	if scopes == "" {
		scopes = DefaultScopes
	}
	expiresIn := tr.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = 60 * 24 * 3600
	}

	tok := store.OAuthToken{
		Provider:     ProviderLinkedIn,
		AccessToken:  tr.AccessToken,
		RefreshToken: tr.RefreshToken,
		PersonURN:    personURN,
		Scopes:       scopes,
		ExpiresAt:    time.Now().UTC().Add(time.Duration(expiresIn) * time.Second),
	}
	if err := s.repo.UpsertOAuthToken(ctx, tok); err != nil {
		return nil, err
	}
	return &tok, nil
}

func (s *OAuthService) fetchPersonURN(ctx context.Context, accessToken string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.endpoints.UserInfoURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := s.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("userinfo request: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("userinfo returned %d: %s", resp.StatusCode, string(body))
	}

	var ui userInfoResponse
	if err := json.Unmarshal(body, &ui); err != nil {
		return "", fmt.Errorf("decode userinfo: %w", err)
	}
	if ui.Sub == "" {
		return "", errors.New("empty sub in userinfo response")
	}
	return "urn:li:person:" + ui.Sub, nil
}

func randomHex(nBytes int) (string, error) {
	b := make([]byte, nBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
