package settings_test

import (
	"context"
	"path/filepath"
	"testing"

	"codeberg.org/matheus/porter/internal/settings"
	"codeberg.org/matheus/porter/internal/store"
)

func TestManagerLiveUpdatesAndMasking(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "live.db")

	db1, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("open db1: %v", err)
	}
	defer db1.Close()

	db2, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("open db2: %v", err)
	}
	defer db2.Close()

	daemonMgr := settings.NewManager(store.NewRepository(db1))
	cliMgr := settings.NewManager(store.NewRepository(db2))

	if err := daemonMgr.SeedDefaults(ctx); err != nil {
		t.Fatalf("seed defaults: %v", err)
	}

	// Simulate CLI changing a setting while daemon is running.
	if err := cliMgr.Set(ctx, settings.KeyDryRun, "true"); err != nil {
		t.Fatalf("cli set dry_run: %v", err)
	}
	if err := cliMgr.Set(ctx, settings.KeyDeepSeekAPIKey, "sk-1234567890abcdef"); err != nil {
		t.Fatalf("cli set api key: %v", err)
	}

	dryRun, err := daemonMgr.GetBool(ctx, settings.KeyDryRun)
	if err != nil || !dryRun {
		t.Fatalf("expected daemon to see dry_run=true immediately, got %v (err=%v)", dryRun, err)
	}

	masked, err := daemonMgr.ListMasked(ctx)
	if err != nil {
		t.Fatalf("list masked: %v", err)
	}
	for _, s := range masked {
		if s.Key == settings.KeyDeepSeekAPIKey && s.Value != "sk-...def" {
			t.Fatalf("expected masked secret 'sk-...def', got %q", s.Value)
		}
	}
}
