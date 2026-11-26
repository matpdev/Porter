package curator

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	readability "codeberg.org/readeck/go-readability/v2"
)

const (
	maxExtractBytes = 2 * 1024 * 1024
	maxExtractChars = 8000
)

type Extractor struct {
	client *http.Client
}

func NewExtractor(client *http.Client) *Extractor {
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	return &Extractor{client: client}
}

// Extract fetches a web article and extracts clean title and readable plain text.
func (e *Extractor) Extract(ctx context.Context, rawURL string) (title string, text string, err error) {
	parsedURL, err := url.Parse(rawURL)
	if err != nil {
		return "", "", fmt.Errorf("parse url: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("User-Agent", "OdradekCurator/1.0 (+https://sidequestlab.dev)")

	resp, err := e.client.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("fetch %s: %w", rawURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", "", fmt.Errorf("fetch %s returned status %d", rawURL, resp.StatusCode)
	}

	limited := io.LimitReader(resp.Body, maxExtractBytes)
	article, err := readability.FromReader(limited, parsedURL)
	if err != nil {
		return "", "", fmt.Errorf("readability parse %s: %w", rawURL, err)
	}

	var buf strings.Builder
	if err := article.RenderText(&buf); err != nil {
		return "", "", fmt.Errorf("render text %s: %w", rawURL, err)
	}

	content := strings.TrimSpace(buf.String())
	if len(content) > maxExtractChars {
		content = content[:maxExtractChars]
	}
	return strings.TrimSpace(article.Title()), content, nil
}
