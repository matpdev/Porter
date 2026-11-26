package telegram_test

import (
	"context"
	"path/filepath"
	"testing"

	"codeberg.org/matheus/porter/internal/porter/telegram"
	"codeberg.org/matheus/porter/internal/settings"
	"codeberg.org/matheus/porter/internal/store"
)

func TestAuthorizationGuard(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "tg.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	repo := store.NewRepository(db)
	mgr := settings.NewManager(repo)
	if err := mgr.SeedDefaults(ctx); err != nil {
		t.Fatalf("seed defaults: %v", err)
	}

	svc := telegram.NewBotService(repo, mgr, nil, nil)

	ok, configured, err := svc.IsAuthorized(ctx, 12345)
	if err != nil || ok || configured {
		t.Fatalf("expected unconfigured unauthorized, got ok=%v configured=%v err=%v", ok, configured, err)
	}

	if err := mgr.Set(ctx, settings.KeyTelegramAdminChatID, "12345"); err != nil {
		t.Fatalf("set admin chat id: %v", err)
	}

	ok, configured, err = svc.IsAuthorized(ctx, 99999)
	if err != nil || ok || !configured {
		t.Fatalf("expected non-admin rejected, got ok=%v configured=%v err=%v", ok, configured, err)
	}

	ok, configured, err = svc.IsAuthorized(ctx, 12345)
	if err != nil || !ok || !configured {
		t.Fatalf("expected admin authorized, got ok=%v configured=%v err=%v", ok, configured, err)
	}
}
