package sidequest

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"codeberg.org/matheus/porter/internal/agents"
	"codeberg.org/matheus/porter/internal/settings"
)

type Publisher struct {
	settings *settings.Manager
}

func NewPublisher(mgr *settings.Manager) *Publisher {
	return &Publisher{settings: mgr}
}

func (p *Publisher) PublishArticle(ctx context.Context, article *agents.SidequestArticle, coverPNGPath string) (string, error) {
	repoPath, err := p.settings.GetString(ctx, settings.KeySidequestRepoPath)
	if err != nil || strings.TrimSpace(repoPath) == "" {
		return "", fmt.Errorf("setting %s is not configured", settings.KeySidequestRepoPath)
	}
	repoPath = strings.TrimSpace(repoPath)

	draft, _ := p.settings.GetBool(ctx, settings.KeySidequestDraft)
	dryRun, _ := p.settings.GetBool(ctx, settings.KeyDryRun)
	autoCommit, _ := p.settings.GetBool(ctx, settings.KeySidequestAutoCommit)

	blogDir := filepath.Join(repoPath, "src", "content", "blog", "en")
	if err := os.MkdirAll(blogDir, 0o755); err != nil {
		return "", fmt.Errorf("create blog dir %s: %w", blogDir, err)
	}

	slug := sanitizeSlug(article.Slug)
	mdxRelPath := filepath.ToSlash(filepath.Join("src", "content", "blog", "en", slug+".mdx"))
	mdxFullPath := filepath.Join(blogDir, slug+".mdx")

	if dryRun {
		return "dryrun:" + mdxRelPath, nil
	}

	var coverLine string
	if coverPNGPath != "" {
		coverFile := slug + "-cover.png"
		if err := copyFile(coverPNGPath, filepath.Join(blogDir, coverFile)); err == nil {
			coverLine = fmt.Sprintf("cover: ./%s\n", coverFile)
			if article.CoverAlt != "" {
				coverLine += fmt.Sprintf("coverAlt: %q\n", article.CoverAlt)
			}
		}
	}

	var sb strings.Builder
	sb.WriteString("---\n")
	sb.WriteString(fmt.Sprintf("title: %q\n", article.Title))
	sb.WriteString(fmt.Sprintf("description: %q\n", article.Description))
	sb.WriteString(fmt.Sprintf("date: %s\n", time.Now().UTC().Format("2006-01-02")))
	sb.WriteString(fmt.Sprintf("type: %s\n", article.Type))
	sb.WriteString(fmt.Sprintf("area: %s\n", article.Area))
	sb.WriteString("tags:\n")
	for _, t := range article.Tags {
		sb.WriteString(fmt.Sprintf("  - %q\n", t))
	}
	sb.WriteString("stack:\n")
	for _, s := range article.Stack {
		sb.WriteString(fmt.Sprintf("  - %q\n", s))
	}
	if coverLine != "" {
		sb.WriteString(coverLine)
	}
	sb.WriteString(fmt.Sprintf("draft: %v\n", draft))
	sb.WriteString("---\n\n")
	sb.WriteString(strings.TrimSpace(article.MarkdownBody))
	sb.WriteString("\n")

	if err := os.WriteFile(mdxFullPath, []byte(sb.String()), 0o644); err != nil {
		return "", fmt.Errorf("write mdx %s: %w", mdxFullPath, err)
	}

	if autoCommit {
		_ = gitCommit(ctx, repoPath, slug)
	}

	return mdxRelPath, nil
}

func sanitizeSlug(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, " ", "-")
	s = strings.ReplaceAll(s, "/", "-")
	if s == "" {
		return fmt.Sprintf("post-%d", time.Now().Unix())
	}
	return s
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}

func gitCommit(ctx context.Context, repoPath, slug string) error {
	add := exec.CommandContext(ctx, "git", "-C", repoPath, "add", filepath.Join("src", "content", "blog", "en"))
	if err := add.Run(); err != nil {
		return err
	}
	commit := exec.CommandContext(ctx, "git", "-C", repoPath, "commit", "-m", "feat(blog): add "+slug)
	return commit.Run()
}
