package settings

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"codeberg.org/matheus/porter/internal/store"
)

const (
	KeyTelegramBotToken          = "telegram.bot_token"
	KeyTelegramAdminChatID       = "telegram.admin_chat_id"
	KeyDeepSeekAPIKey            = "ai.deepseek_api_key"
	KeyDeepSeekModel             = "ai.deepseek_model"
	KeyWriterTone                = "writer.tone"
	KeyGeminiAPIKey              = "ai.gemini_api_key"
	KeyGeminiModel               = "ai.gemini_model"
	KeyQAProvider                = "ai.qa_provider"
	KeyAgyBin                    = "ai.agy_bin"
	KeyLinkedInClientID          = "linkedin.client_id"
	KeyLinkedInClientSecret      = "linkedin.client_secret"
	KeyLinkedInRedirectURI       = "linkedin.redirect_uri"
	KeyCronSchedule              = "pipeline.cron_schedule"
	KeyReviewReminderSchedule    = "pipeline.review_reminder_schedule"
	KeyAutoTrustAfterApprovals   = "pipeline.auto_trust_after_approvals"
	KeyMinConfidenceScore        = "pipeline.min_confidence_score"
	KeyTelegramRequireApproval   = "pipeline.telegram_require_approval"
	KeyDryRun                    = "pipeline.dry_run"
	KeySidequestEnabled          = "sidequest.enabled"
	KeySidequestRepoPath         = "sidequest.repo_path"
	KeySidequestAutoCommit       = "sidequest.auto_commit"
	KeySidequestDraft            = "sidequest.draft"
	KeyCuratorRSSFeeds           = "curator.rss_feeds"
	KeyCuratorHNMinScore         = "curator.hn_min_score"
	KeyCuratorGitHubUsername     = "curator.github_username"
	KeyCuratorGitHubToken        = "curator.github_token"
	KeyCuratorCodebergUsername   = "curator.codeberg_username"
	KeyCuratorCodebergToken      = "curator.codeberg_token"
	KeyCuratorGitRepos           = "curator.git_repos"
)

type definition struct {
	Key          string
	DefaultValue string
	IsSecret     bool
}

var defaults = []definition{
	{Key: KeyTelegramBotToken, DefaultValue: "", IsSecret: true},
	{Key: KeyTelegramAdminChatID, DefaultValue: "0", IsSecret: false},
	{Key: KeyDeepSeekAPIKey, DefaultValue: "", IsSecret: true},
	{Key: KeyDeepSeekModel, DefaultValue: "deepseek-chat", IsSecret: false},
	{Key: KeyWriterTone, DefaultValue: "technical", IsSecret: false},
	{Key: KeyGeminiAPIKey, DefaultValue: "", IsSecret: true},
	{Key: KeyGeminiModel, DefaultValue: "gemini-2.5-flash", IsSecret: false},
	{Key: KeyQAProvider, DefaultValue: "auto", IsSecret: false},
	{Key: KeyAgyBin, DefaultValue: "agy", IsSecret: false},
	{Key: KeyLinkedInClientID, DefaultValue: "", IsSecret: false},
	{Key: KeyLinkedInClientSecret, DefaultValue: "", IsSecret: true},
	{Key: KeyLinkedInRedirectURI, DefaultValue: "http://127.0.0.1:8787/oauth/linkedin/callback", IsSecret: false},
	{Key: KeyCronSchedule, DefaultValue: "0 8 * * 2,4", IsSecret: false},
	{Key: KeyReviewReminderSchedule, DefaultValue: "0 10,18 * * *", IsSecret: false},
	{Key: KeyAutoTrustAfterApprovals, DefaultValue: "2", IsSecret: false},
	{Key: KeyMinConfidenceScore, DefaultValue: "8", IsSecret: false},
	{Key: KeyTelegramRequireApproval, DefaultValue: "true", IsSecret: false},
	{Key: KeyDryRun, DefaultValue: "false", IsSecret: false},
	{Key: KeySidequestEnabled, DefaultValue: "false", IsSecret: false},
	{Key: KeySidequestRepoPath, DefaultValue: "/Users/matheus/Projects/sidequestlab.dev", IsSecret: false},
	{Key: KeySidequestAutoCommit, DefaultValue: "true", IsSecret: false},
	{Key: KeySidequestDraft, DefaultValue: "false", IsSecret: false},
	{Key: KeyCuratorRSSFeeds, DefaultValue: "https://go.dev/blog/feed.atom,https://martinfowler.com/feed.atom,https://lobste.rs/rss", IsSecret: false},
	{Key: KeyCuratorHNMinScore, DefaultValue: "80", IsSecret: false},
	{Key: KeyCuratorGitHubUsername, DefaultValue: "", IsSecret: false},
	{Key: KeyCuratorGitHubToken, DefaultValue: "", IsSecret: true},
	{Key: KeyCuratorCodebergUsername, DefaultValue: "", IsSecret: false},
	{Key: KeyCuratorCodebergToken, DefaultValue: "", IsSecret: true},
	{Key: KeyCuratorGitRepos, DefaultValue: "", IsSecret: false},
}

// Manager provides live, restart-free access to runtime settings backed by SQLite.
type Manager struct {
	repo *store.Repository
}

func NewManager(repo *store.Repository) *Manager {
	return &Manager{repo: repo}
}

// SeedDefaults populates missing settings keys with safe defaults without overwriting existing values.
func (m *Manager) SeedDefaults(ctx context.Context) error {
	for _, d := range defaults {
		if err := m.repo.EnsureSettingDefault(ctx, d.Key, d.DefaultValue, d.IsSecret); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) Set(ctx context.Context, key, value string) error {
	isSecret := isSecretKey(key)
	return m.repo.SetSetting(ctx, key, value, isSecret)
}

func (m *Manager) GetString(ctx context.Context, key string) (string, error) {
	s, err := m.repo.GetSetting(ctx, key)
	if err != nil {
		return "", err
	}
	return s.Value, nil
}

func (m *Manager) GetBool(ctx context.Context, key string) (bool, error) {
	val, err := m.GetString(ctx, key)
	if err != nil {
		return false, err
	}
	parsed, err := strconv.ParseBool(strings.TrimSpace(val))
	if err != nil {
		return false, fmt.Errorf("setting %s=%q is not a valid bool: %w", key, val, err)
	}
	return parsed, nil
}

func (m *Manager) GetInt(ctx context.Context, key string) (int, error) {
	val, err := m.GetString(ctx, key)
	if err != nil {
		return 0, err
	}
	parsed, err := strconv.Atoi(strings.TrimSpace(val))
	if err != nil {
		return 0, fmt.Errorf("setting %s=%q is not a valid int: %w", key, val, err)
	}
	return parsed, nil
}

func (m *Manager) GetInt64(ctx context.Context, key string) (int64, error) {
	val, err := m.GetString(ctx, key)
	if err != nil {
		return 0, err
	}
	parsed, err := strconv.ParseInt(strings.TrimSpace(val), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("setting %s=%q is not a valid int64: %w", key, val, err)
	}
	return parsed, nil
}

// ListMasked returns all settings with secret values masked for safe display in CLI or Telegram.
func (m *Manager) ListMasked(ctx context.Context) ([]store.Setting, error) {
	all, err := m.repo.ListSettings(ctx)
	if err != nil {
		return nil, err
	}
	for i := range all {
		if all[i].IsSecret {
			all[i].Value = maskSecret(all[i].Value)
		}
	}
	return all, nil
}

func isSecretKey(key string) bool {
	for _, d := range defaults {
		if d.Key == key {
			return d.IsSecret
		}
	}
	lower := strings.ToLower(key)
	return strings.Contains(lower, "secret") ||
		strings.Contains(lower, "token") ||
		strings.Contains(lower, "api_key")
}

func maskSecret(v string) string {
	if v == "" {
		return "<unset>"
	}
	if len(v) <= 6 {
		return "******"
	}
	return v[:3] + "..." + v[len(v)-3:]
}
