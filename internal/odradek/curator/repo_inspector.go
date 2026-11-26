package curator

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"
)

type RepoRequest struct {
	Forge          string // "github" or "codeberg"
	ForgeSpecified bool
	Owner          string
	Repo           string
	Branch         string
}

func (r RepoRequest) FullName() string {
	return r.Owner + "/" + r.Repo
}

var (
	repoSlugRe   = regexp.MustCompile(`\b([a-zA-Z0-9_.-]+/[a-zA-Z0-9_.-]+)\b`)
	branchHintRe = regexp.MustCompile(`(?i)\b(?:branch|br)\b(?:\s+\w+){0,2}?\s*[:=]?\s*([a-zA-Z0-9_./-]+)`)
)

// ParseRepoIntent detects an explicit GitHub/Codeberg repository and optional branch in any language.
func ParseRepoIntent(text string) (RepoRequest, bool) {
	lower := strings.ToLower(text)

	matches := repoSlugRe.FindAllStringSubmatch(text, -1)
	var owner, repo string
	for _, m := range matches {
		parts := strings.SplitN(m[1], "/", 2)
		if len(parts) != 2 || strings.Contains(parts[0], ".") {
			continue
		}
		owner, repo = parts[0], strings.TrimRight(parts[1], ".,;:!?)")
		break
	}
	if owner == "" || repo == "" {
		return RepoRequest{}, false
	}

	forge := "github"
	forgeSpecified := false
	if strings.Contains(lower, "codeberg") {
		forge = "codeberg"
		forgeSpecified = true
	} else if strings.Contains(lower, "github") {
		forgeSpecified = true
	}

	var branch string
	if bm := branchHintRe.FindStringSubmatch(text); len(bm) >= 2 {
		candidate := strings.TrimRight(bm[1], ".,;:!?)")
		// Ignore stop-words if user wrote "on branch" without a name
		switch strings.ToLower(candidate) {
		case "and", "e", "with", "com", "on", "no", "na", "in", "em":
		default:
			branch = candidate
		}
	}

	return RepoRequest{
		Forge:          forge,
		ForgeSpecified: forgeSpecified,
		Owner:          owner,
		Repo:           repo,
		Branch:         branch,
	}, true
}

type RepoInspector struct {
	client          *http.Client
	githubBaseURL   string
	codebergBaseURL string
}

func NewRepoInspector(client *http.Client) *RepoInspector {
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	return &RepoInspector{
		client:          client,
		githubBaseURL:   "https://api.github.com",
		codebergBaseURL: "https://codeberg.org/api/v1",
	}
}

func (ri *RepoInspector) WithBaseURLs(githubBase, codebergBase string) *RepoInspector {
	if githubBase != "" {
		ri.githubBaseURL = strings.TrimRight(githubBase, "/")
	}
	if codebergBase != "" {
		ri.codebergBaseURL = strings.TrimRight(codebergBase, "/")
	}
	return ri
}

type repoMeta struct {
	FullName      string `json:"full_name"`
	Description   string `json:"description"`
	DefaultBranch string `json:"default_branch"`
	Fork          bool   `json:"fork"`
	HTMLURL       string `json:"html_url"`
	Parent        *struct {
		FullName      string `json:"full_name"`
		DefaultBranch string `json:"default_branch"`
		Owner         struct {
			Login string `json:"login"`
		} `json:"owner"`
	} `json:"parent"`
}

type compareResponse struct {
	AheadBy int `json:"ahead_by"`
	Commits []struct {
		Commit struct {
			Message string `json:"message"`
		} `json:"commit"`
	} `json:"commits"`
	Files []struct {
		Filename  string `json:"filename"`
		Status    string `json:"status"`
		Additions int    `json:"additions"`
		Deletions int    `json:"deletions"`
	} `json:"files"`
}

// Inspect fetches repository metadata, branch commits, and diff summary against base/upstream.
func (ri *RepoInspector) Inspect(ctx context.Context, req RepoRequest, token string) (repoURL string, summary string, err error) {
	baseURL := ri.githubBaseURL
	if req.Forge == "codeberg" {
		baseURL = ri.codebergBaseURL
	}

	meta, err := ri.fetchMeta(ctx, baseURL, req.Forge, req.FullName(), token)
	if err != nil {
		return "", "", err
	}

	branch := req.Branch
	if branch == "" {
		branch = meta.DefaultBranch
	}
	if branch == "" {
		branch = "main"
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Repository: %s (%s)\n", req.FullName(), req.Forge))
	if meta.HTMLURL != "" {
		sb.WriteString(fmt.Sprintf("Canonical URL: %s\n", meta.HTMLURL))
	}
	sb.WriteString(fmt.Sprintf("Branch inspected: %s (default: %s, fork: %v)\n", branch, meta.DefaultBranch, meta.Fork))
	if meta.Description != "" {
		sb.WriteString(fmt.Sprintf("Description: %s\n", meta.Description))
	}
	if meta.Fork && meta.Parent != nil {
		sb.WriteString(fmt.Sprintf("Forked from: %s (base branch: %s)\n", meta.Parent.FullName, meta.Parent.DefaultBranch))
	}
	sb.WriteString("\n")

	if tree, err := ri.fetchRootContents(ctx, baseURL, req.Forge, req.FullName(), branch, token); err == nil && len(tree) > 0 {
		sb.WriteString("Repository root files/dirs:\n")
		for _, entry := range tree {
			sb.WriteString("- " + entry + "\n")
		}
		sb.WriteString("\n")
	}

	if readme, err := ri.fetchReadme(ctx, baseURL, req.Forge, req.FullName(), branch, token); err == nil && readme != "" {
		if len(readme) > 6000 {
			readme = readme[:6000]
		}
		sb.WriteString("README:\n")
		sb.WriteString(readme)
		sb.WriteString("\n\n")
	}

	// Try compare diff when inspecting a non-default branch or a fork.
	var compareSpec string
	if req.Forge == "github" && meta.Fork && meta.Parent != nil && meta.Parent.Owner.Login != "" {
		parentBranch := meta.Parent.DefaultBranch
		if parentBranch == "" {
			parentBranch = "main"
		}
		compareSpec = fmt.Sprintf("%s:%s...%s:%s", meta.Parent.Owner.Login, parentBranch, req.Owner, branch)
	} else if meta.DefaultBranch != "" && branch != meta.DefaultBranch {
		compareSpec = fmt.Sprintf("%s...%s", meta.DefaultBranch, branch)
	}

	if compareSpec != "" {
		if cmp, err := ri.fetchCompare(ctx, baseURL, req.Forge, req.FullName(), compareSpec, token); err == nil {
			sb.WriteString(fmt.Sprintf("Comparison (%s): %d commits ahead\n", compareSpec, cmp.AheadBy))
			if len(cmp.Commits) > 0 {
				sb.WriteString("Branch commits:\n")
				limit := len(cmp.Commits)
				if limit > 20 {
					limit = 20
				}
				for _, c := range cmp.Commits[:limit] {
					line := strings.Split(strings.TrimSpace(c.Commit.Message), "\n")[0]
					if line != "" {
						sb.WriteString("- " + line + "\n")
					}
				}
			}
			if len(cmp.Files) > 0 {
				sb.WriteString("\nChanged files:\n")
				limit := len(cmp.Files)
				if limit > 25 {
					limit = 25
				}
				for _, f := range cmp.Files[:limit] {
					sb.WriteString(fmt.Sprintf("- %s (%s, +%d/-%d)\n", f.Filename, f.Status, f.Additions, f.Deletions))
				}
			}
			sb.WriteString("\n")
		}
	}

	if commits, err := ri.fetchBranchCommits(ctx, baseURL, req.Forge, req.FullName(), branch, token); err == nil && len(commits) > 0 {
		sb.WriteString(fmt.Sprintf("Recent commits on %s:\n", branch))
		for _, c := range commits {
			sb.WriteString("- " + c + "\n")
		}
	}

	return meta.HTMLURL, strings.TrimSpace(sb.String()), nil
}

func (ri *RepoInspector) doAuthGet(ctx context.Context, u, forge, token string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "PorterOdradek/1.0")
	if token != "" {
		if forge == "codeberg" {
			req.Header.Set("Authorization", "token "+token)
		} else {
			req.Header.Set("Authorization", "Bearer "+token)
		}
	}
	return ri.client.Do(req)
}

func (ri *RepoInspector) fetchMeta(ctx context.Context, baseURL, forge, fullName, token string) (*repoMeta, error) {
	resp, err := ri.doAuthGet(ctx, fmt.Sprintf("%s/repos/%s", baseURL, fullName), forge, token)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("repo %s returned status %d", fullName, resp.StatusCode)
	}
	var m repoMeta
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		return nil, err
	}
	return &m, nil
}

func (ri *RepoInspector) fetchCompare(ctx context.Context, baseURL, forge, fullName, spec, token string) (*compareResponse, error) {
	resp, err := ri.doAuthGet(ctx, fmt.Sprintf("%s/repos/%s/compare/%s", baseURL, fullName, spec), forge, token)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("compare %s returned %d", spec, resp.StatusCode)
	}
	var c compareResponse
	if err := json.NewDecoder(resp.Body).Decode(&c); err != nil {
		return nil, err
	}
	return &c, nil
}

func (ri *RepoInspector) fetchBranchCommits(ctx context.Context, baseURL, forge, fullName, branch, token string) ([]string, error) {
	resp, err := ri.doAuthGet(ctx, fmt.Sprintf("%s/repos/%s/commits?sha=%s&per_page=15", baseURL, fullName, branch), forge, token)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("commits on %s returned %d", branch, resp.StatusCode)
	}
	var items []ghCommitItem
	if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
		return nil, err
	}
	var out []string
	for _, it := range items {
		line := strings.Split(strings.TrimSpace(it.Commit.Message), "\n")[0]
		if line != "" {
			out = append(out, line)
		}
	}
	return out, nil
}

func (ri *RepoInspector) fetchRootContents(ctx context.Context, baseURL, forge, fullName, branch, token string) ([]string, error) {
	u := fmt.Sprintf("%s/repos/%s/contents?ref=%s", baseURL, fullName, branch)
	resp, err := ri.doAuthGet(ctx, u, forge, token)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("contents status %d", resp.StatusCode)
	}
	var entries []struct {
		Name string `json:"name"`
		Type string `json:"type"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&entries); err != nil {
		return nil, err
	}
	var out []string
	for i, e := range entries {
		if i >= 30 {
			break
		}
		out = append(out, fmt.Sprintf("%s (%s)", e.Name, e.Type))
	}
	return out, nil
}

func (ri *RepoInspector) fetchReadme(ctx context.Context, baseURL, forge, fullName, branch, token string) (string, error) {
	var u string
	if forge == "codeberg" {
		u = fmt.Sprintf("%s/repos/%s/raw/README.md?ref=%s", baseURL, fullName, branch)
	} else {
		u = fmt.Sprintf("%s/repos/%s/readme?ref=%s", baseURL, fullName, branch)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "PorterOdradek/1.0")
	if forge != "codeberg" {
		req.Header.Set("Accept", "application/vnd.github.raw+json")
	}
	if token != "" {
		if forge == "codeberg" {
			req.Header.Set("Authorization", "token "+token)
		} else {
			req.Header.Set("Authorization", "Bearer "+token)
		}
	}
	resp, err := ri.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("readme status %d", resp.StatusCode)
	}
	buf := make([]byte, 8192)
	n, _ := resp.Body.Read(buf)
	return strings.TrimSpace(string(buf[:n])), nil
}
