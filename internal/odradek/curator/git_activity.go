package curator

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type GitActivityCollector struct {
	client          *http.Client
	githubBaseURL   string
	codebergBaseURL string
}

func NewGitActivityCollector(client *http.Client) *GitActivityCollector {
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	return &GitActivityCollector{
		client:          client,
		githubBaseURL:   "https://api.github.com",
		codebergBaseURL: "https://codeberg.org/api/v1",
	}
}

func (g *GitActivityCollector) WithBaseURLs(githubBase, codebergBase string) *GitActivityCollector {
	if githubBase != "" {
		g.githubBaseURL = strings.TrimRight(githubBase, "/")
	}
	if codebergBase != "" {
		g.codebergBaseURL = strings.TrimRight(codebergBase, "/")
	}
	return g
}

type RepoUpdate struct {
	Forge   string // "github" or "codeberg"
	Repo    string
	Commits []string
}

type GitSummaryConfig struct {
	GitHubUsername   string
	GitHubToken      string
	CodebergUsername string
	CodebergToken    string
	Repos            []string // optional "owner/repo" list
	Since            time.Time
}

// CollectWeeklySummary gathers commit activity across GitHub and Codeberg since cfg.Since.
func (g *GitActivityCollector) CollectWeeklySummary(ctx context.Context, cfg GitSummaryConfig) ([]RepoUpdate, error) {
	if cfg.Since.IsZero() {
		cfg.Since = time.Now().UTC().AddDate(0, 0, -7)
	}

	var updates []RepoUpdate

	if len(cfg.Repos) > 0 {
		for _, repo := range cfg.Repos {
			r := strings.TrimSpace(repo)
			if r == "" {
				continue
			}
			if cfg.GitHubUsername != "" || cfg.GitHubToken != "" {
				if commits, err := g.fetchRepoCommits(ctx, g.githubBaseURL, r, cfg.GitHubToken, cfg.Since); err == nil && len(commits) > 0 {
					updates = append(updates, RepoUpdate{Forge: "github", Repo: r, Commits: commits})
				}
			}
			if cfg.CodebergUsername != "" || cfg.CodebergToken != "" {
				if commits, err := g.fetchRepoCommits(ctx, g.codebergBaseURL, r, cfg.CodebergToken, cfg.Since); err == nil && len(commits) > 0 {
					updates = append(updates, RepoUpdate{Forge: "codeberg", Repo: r, Commits: commits})
				}
			}
		}
		return updates, nil
	}

	if cfg.GitHubToken != "" {
		gh, err := g.fetchGitHubUserReposCommits(ctx, cfg.GitHubToken, cfg.Since)
		if err == nil {
			updates = append(updates, gh...)
		}
	} else if cfg.GitHubUsername != "" {
		gh, err := g.fetchGitHubEvents(ctx, cfg.GitHubUsername, "", cfg.Since)
		if err == nil {
			updates = append(updates, gh...)
		}
	}

	if cfg.CodebergToken != "" {
		cb, err := g.fetchCodebergUserReposCommits(ctx, cfg.CodebergToken, cfg.Since)
		if err == nil {
			updates = append(updates, cb...)
		}
	} else if cfg.CodebergUsername != "" {
		cb, err := g.fetchCodebergFeeds(ctx, cfg.CodebergUsername, "", cfg.Since)
		if err == nil {
			updates = append(updates, cb...)
		}
	}

	return updates, nil
}

func FormatRepoUpdates(updates []RepoUpdate) string {
	var sb strings.Builder
	sb.WriteString("Weekly repository activity:\n\n")
	for _, u := range updates {
		sb.WriteString(fmt.Sprintf("## [%s] %s\n", u.Forge, u.Repo))
		for _, c := range u.Commits {
			sb.WriteString("- " + c + "\n")
		}
		sb.WriteString("\n")
	}
	return strings.TrimSpace(sb.String())
}

type ghCommitItem struct {
	Commit struct {
		Message string `json:"message"`
	} `json:"commit"`
}

func (g *GitActivityCollector) fetchRepoCommits(ctx context.Context, baseURL, repo, token string, since time.Time) ([]string, error) {
	u := fmt.Sprintf("%s/repos/%s/commits?since=%s&per_page=20", baseURL, repo, since.UTC().Format(time.RFC3339))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "PorterOdradek/1.0")
	if token != "" {
		if strings.Contains(baseURL, "codeberg.org") {
			req.Header.Set("Authorization", "token "+token)
		} else {
			req.Header.Set("Authorization", "Bearer "+token)
		}
	}

	resp, err := g.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("commits %s status %d", repo, resp.StatusCode)
	}

	var items []ghCommitItem
	if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
		return nil, err
	}

	var commits []string
	for _, it := range items {
		firstLine := strings.Split(strings.TrimSpace(it.Commit.Message), "\n")[0]
		if firstLine != "" {
			commits = append(commits, firstLine)
		}
	}
	return commits, nil
}

type ghEvent struct {
	Type      string    `json:"type"`
	CreatedAt time.Time `json:"created_at"`
	Repo      struct {
		Name string `json:"name"`
	} `json:"repo"`
	Payload struct {
		Commits []struct {
			Message string `json:"message"`
		} `json:"commits"`
	} `json:"payload"`
}

func (g *GitActivityCollector) fetchGitHubEvents(ctx context.Context, username, token string, since time.Time) ([]RepoUpdate, error) {
	u := fmt.Sprintf("%s/users/%s/events/public?per_page=50", g.githubBaseURL, username)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "PorterOdradek/1.0")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := g.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github events status %d", resp.StatusCode)
	}

	var events []ghEvent
	if err := json.NewDecoder(resp.Body).Decode(&events); err != nil {
		return nil, err
	}

	byRepo := make(map[string][]string)
	var order []string
	for _, ev := range events {
		if ev.Type != "PushEvent" || ev.CreatedAt.Before(since) {
			continue
		}
		if _, exists := byRepo[ev.Repo.Name]; !exists {
			order = append(order, ev.Repo.Name)
		}
		for _, c := range ev.Payload.Commits {
			line := strings.Split(strings.TrimSpace(c.Message), "\n")[0]
			if line != "" {
				byRepo[ev.Repo.Name] = append(byRepo[ev.Repo.Name], line)
			}
		}
	}

	var out []RepoUpdate
	for _, repo := range order {
		if len(byRepo[repo]) > 0 {
			out = append(out, RepoUpdate{Forge: "github", Repo: repo, Commits: byRepo[repo]})
		}
	}
	return out, nil
}

type repoSummary struct {
	FullName  string    `json:"full_name"`
	PushedAt  time.Time `json:"pushed_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (g *GitActivityCollector) fetchGitHubUserReposCommits(ctx context.Context, token string, since time.Time) ([]RepoUpdate, error) {
	u := fmt.Sprintf("%s/user/repos?sort=pushed&direction=desc&per_page=15", g.githubBaseURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "PorterOdradek/1.0")
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := g.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github /user/repos status %d", resp.StatusCode)
	}

	var repos []repoSummary
	if err := json.NewDecoder(resp.Body).Decode(&repos); err != nil {
		return nil, err
	}

	var out []RepoUpdate
	for _, r := range repos {
		ts := r.PushedAt
		if ts.IsZero() {
			ts = r.UpdatedAt
		}
		if !ts.IsZero() && ts.Before(since) {
			continue
		}
		commits, err := g.fetchRepoCommits(ctx, g.githubBaseURL, r.FullName, token, since)
		if err == nil && len(commits) > 0 {
			out = append(out, RepoUpdate{Forge: "github", Repo: r.FullName, Commits: commits})
		}
	}
	return out, nil
}

func (g *GitActivityCollector) fetchCodebergUserReposCommits(ctx context.Context, token string, since time.Time) ([]RepoUpdate, error) {
	u := fmt.Sprintf("%s/user/repos?limit=15", g.codebergBaseURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "PorterOdradek/1.0")
	req.Header.Set("Authorization", "token "+token)

	resp, err := g.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("codeberg /user/repos status %d", resp.StatusCode)
	}

	var repos []repoSummary
	if err := json.NewDecoder(resp.Body).Decode(&repos); err != nil {
		return nil, err
	}

	var out []RepoUpdate
	for _, r := range repos {
		if !r.UpdatedAt.IsZero() && r.UpdatedAt.Before(since) {
			continue
		}
		commits, err := g.fetchRepoCommits(ctx, g.codebergBaseURL, r.FullName, token, since)
		if err == nil && len(commits) > 0 {
			out = append(out, RepoUpdate{Forge: "codeberg", Repo: r.FullName, Commits: commits})
		}
	}
	return out, nil
}

type forgejoFeedItem struct {
	OpType  string    `json:"op_type"`
	Created time.Time `json:"created"`
	Repo    struct {
		FullName string `json:"full_name"`
	} `json:"repo"`
	Content string `json:"content"`
}

type forgejoPushContent struct {
	Commits []struct {
		Message string `json:"Message"`
	} `json:"Commits"`
}

func (g *GitActivityCollector) fetchCodebergFeeds(ctx context.Context, username, token string, since time.Time) ([]RepoUpdate, error) {
	u := fmt.Sprintf("%s/users/%s/activities/feeds?limit=50", g.codebergBaseURL, username)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "PorterOdradek/1.0")
	if token != "" {
		req.Header.Set("Authorization", "token "+token)
	}

	resp, err := g.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("codeberg feeds status %d", resp.StatusCode)
	}

	var items []forgejoFeedItem
	if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
		return nil, err
	}

	byRepo := make(map[string][]string)
	var order []string
	for _, it := range items {
		if it.OpType != "commit_repo" || it.Created.Before(since) {
			continue
		}
		repoName := it.Repo.FullName
		if _, exists := byRepo[repoName]; !exists {
			order = append(order, repoName)
		}
		var pc forgejoPushContent
		if err := json.Unmarshal([]byte(it.Content), &pc); err == nil {
			for _, c := range pc.Commits {
				line := strings.Split(strings.TrimSpace(c.Message), "\n")[0]
				if line != "" {
					byRepo[repoName] = append(byRepo[repoName], line)
				}
			}
		}
	}

	var out []RepoUpdate
	for _, repo := range order {
		if len(byRepo[repo]) > 0 {
			out = append(out, RepoUpdate{Forge: "codeberg", Repo: repo, Commits: byRepo[repo]})
		}
	}
	return out, nil
}
