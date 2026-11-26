package curator

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/mmcdole/gofeed"
)

type FeedCandidate struct {
	SourceType  string // "hackernews" or "rss"
	Title       string
	URL         string
	Description string
	Score       int
}

type FeedCollector struct {
	client    *http.Client
	hnBaseURL string
	parser    *gofeed.Parser
}

func NewFeedCollector(client *http.Client) *FeedCollector {
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	p := gofeed.NewParser()
	p.Client = client
	return &FeedCollector{
		client:    client,
		hnBaseURL: "https://hacker-news.firebaseio.com/v0",
		parser:    p,
	}
}

func (f *FeedCollector) WithHNBaseURL(baseURL string) *FeedCollector {
	f.hnBaseURL = strings.TrimRight(baseURL, "/")
	return f
}

type hnItem struct {
	ID    int64  `json:"id"`
	Title string `json:"title"`
	URL   string `json:"url"`
	Score int    `json:"score"`
	Type  string `json:"type"`
}

func (f *FeedCollector) FetchHackerNews(ctx context.Context, minScore, maxItems int) ([]FeedCandidate, error) {
	if maxItems <= 0 {
		maxItems = 25
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.hnBaseURL+"/topstories.json", nil)
	if err != nil {
		return nil, err
	}
	resp, err := f.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("hn topstories: %w", err)
	}
	defer resp.Body.Close()

	var ids []int64
	if err := json.NewDecoder(resp.Body).Decode(&ids); err != nil {
		return nil, fmt.Errorf("decode hn topstories: %w", err)
	}
	if len(ids) > maxItems {
		ids = ids[:maxItems]
	}

	var out []FeedCandidate
	for _, id := range ids {
		item, err := f.fetchHNItem(ctx, id)
		if err != nil || item.URL == "" || item.Score < minScore {
			continue
		}
		out = append(out, FeedCandidate{
			SourceType: "hackernews",
			Title:      item.Title,
			URL:        item.URL,
			Score:      item.Score,
		})
	}
	return out, nil
}

func (f *FeedCollector) fetchHNItem(ctx context.Context, id int64) (*hnItem, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/item/%d.json", f.hnBaseURL, id), nil)
	if err != nil {
		return nil, err
	}
	resp, err := f.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var item hnItem
	if err := json.NewDecoder(resp.Body).Decode(&item); err != nil {
		return nil, err
	}
	return &item, nil
}

func (f *FeedCollector) FetchRSS(ctx context.Context, feedURLs []string, maxPerFeed int) []FeedCandidate {
	if maxPerFeed <= 0 {
		maxPerFeed = 10
	}
	var out []FeedCandidate
	for _, rawURL := range feedURLs {
		u := strings.TrimSpace(rawURL)
		if u == "" {
			continue
		}
		feed, err := f.parser.ParseURLWithContext(u, ctx)
		if err != nil {
			continue
		}
		limit := maxPerFeed
		if len(feed.Items) < limit {
			limit = len(feed.Items)
		}
		for _, item := range feed.Items[:limit] {
			if item.Link == "" || item.Title == "" {
				continue
			}
			out = append(out, FeedCandidate{
				SourceType:  "rss",
				Title:       strings.TrimSpace(item.Title),
				URL:         strings.TrimSpace(item.Link),
				Description: strings.TrimSpace(item.Description),
			})
		}
	}
	return out
}
