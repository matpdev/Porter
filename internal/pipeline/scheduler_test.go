package pipeline_test

import (
	"context"
	"testing"

	"codeberg.org/matheus/porter/internal/pipeline"
	"codeberg.org/matheus/porter/internal/settings"
)

func TestSchedulerReloadsLiveWithoutRestart(t *testing.T) {
	ctx := context.Background()
	repo, mgr := setupOrchDB(t)
	orch := pipeline.NewOrchestrator(repo, mgr, nil, &mockWriter{}, nil, &mockQA{}, &mockLinkedIn{}, nil, &mockTelegram{}, nil)

	sched := pipeline.NewScheduler(mgr, orch, nil)
	if err := sched.Reload(ctx); err != nil {
		t.Fatalf("initial reload: %v", err)
	}
	defer sched.Stop()

	c1, _ := sched.CurrentSchedules()
	if c1 != "0 8 * * 2,4" {
		t.Fatalf("unexpected initial cron: %q", c1)
	}

	if err := mgr.Set(ctx, settings.KeyCronSchedule, "30 9 * * 1,3,5"); err != nil {
		t.Fatalf("set cron schedule: %v", err)
	}
	if err := sched.Reload(ctx); err != nil {
		t.Fatalf("live reload: %v", err)
	}

	c2, _ := sched.CurrentSchedules()
	if c2 != "30 9 * * 1,3,5" {
		t.Fatalf("expected updated cron '30 9 * * 1,3,5', got %q", c2)
	}
}
