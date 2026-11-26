package linkedin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"codeberg.org/matheus/porter/internal/settings"
	"codeberg.org/matheus/porter/internal/store"
)

const linkedInVersion = "202501"

type Client struct {
	repo     *store.Repository
	settings *settings.Manager
	http     *http.Client
	baseURL  string
}

func NewClient(repo *store.Repository, mgr *settings.Manager, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	return &Client{
		repo:     repo,
		settings: mgr,
		http:     httpClient,
		baseURL:  "https://api.linkedin.com",
	}
}

func (c *Client) WithBaseURL(baseURL string) *Client {
	c.baseURL = strings.TrimRight(baseURL, "/")
	return c
}

func (c *Client) PublishPost(ctx context.Context, postID, text, pngPath string) (string, error) {
	if dryRun, err := c.settings.GetBool(ctx, settings.KeyDryRun); err == nil && dryRun {
		return "urn:li:dryrun:" + postID, nil
	}

	tok, err := c.repo.GetOAuthToken(ctx, ProviderLinkedIn)
	if err != nil {
		return "", fmt.Errorf("linkedin token missing (run /auth): %w", err)
	}
	if time.Now().UTC().After(tok.ExpiresAt) {
		return "", errors.New("linkedin token expired (run /auth to reconnect)")
	}

	var imageURN string
	if pngPath != "" {
		imageURN, err = c.uploadImage(ctx, tok.AccessToken, tok.PersonURN, pngPath)
		if err != nil {
			return "", fmt.Errorf("upload image: %w", err)
		}
	}

	payload := map[string]any{
		"author":     tok.PersonURN,
		"commentary": text,
		"visibility": "PUBLIC",
		"distribution": map[string]any{
			"feedDistribution":               "MAIN_FEED",
			"targetEntities":                 []any{},
			"thirdPartyDistributionChannels": []any{},
		},
		"lifecycleState":            "PUBLISHED",
		"isReshareDisabledByAuthor": false,
	}
	if imageURN != "" {
		payload["content"] = map[string]any{
			"media": map[string]any{
				"id": imageURN,
			},
		}
	}

	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/rest/posts", bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	c.setRestHeaders(req, tok.AccessToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("post /rest/posts: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("linkedin /rest/posts returned %d: %s", resp.StatusCode, string(body))
	}

	urn := resp.Header.Get("x-restli-id")
	if urn == "" {
		urn = "urn:li:post:published"
	}
	return urn, nil
}

type initUploadResponse struct {
	Value struct {
		UploadURL string `json:"uploadUrl"`
		Image     string `json:"image"`
	} `json:"value"`
}

func (c *Client) uploadImage(ctx context.Context, token, personURN, pngPath string) (string, error) {
	initBody, _ := json.Marshal(map[string]any{
		"initializeUploadRequest": map[string]any{
			"owner": personURN,
		},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/rest/images?action=initializeUpload", bytes.NewReader(initBody))
	if err != nil {
		return "", err
	}
	c.setRestHeaders(req, token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("initializeUpload returned %d: %s", resp.StatusCode, string(raw))
	}

	var initResp initUploadResponse
	if err := json.Unmarshal(raw, &initResp); err != nil {
		return "", err
	}
	if initResp.Value.UploadURL == "" || initResp.Value.Image == "" {
		return "", errors.New("incomplete initializeUpload response")
	}

	pngBytes, err := os.ReadFile(pngPath)
	if err != nil {
		return "", err
	}
	putReq, err := http.NewRequestWithContext(ctx, http.MethodPut, initResp.Value.UploadURL, bytes.NewReader(pngBytes))
	if err != nil {
		return "", err
	}
	putReq.Header.Set("Authorization", "Bearer "+token)
	putReq.Header.Set("Content-Type", "image/png")

	putResp, err := c.http.Do(putReq)
	if err != nil {
		return "", err
	}
	defer putResp.Body.Close()
	if putResp.StatusCode < 200 || putResp.StatusCode >= 300 {
		b, _ := io.ReadAll(putResp.Body)
		return "", fmt.Errorf("PUT image returned %d: %s", putResp.StatusCode, string(b))
	}

	return initResp.Value.Image, nil
}

func (c *Client) setRestHeaders(req *http.Request, token string) {
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("LinkedIn-Version", linkedInVersion)
	req.Header.Set("X-Restli-Protocol-Version", "2.0.0")
}
