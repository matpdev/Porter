package pipeline

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"codeberg.org/matheus/porter/internal/settings"
	"github.com/robfig/cron/v3"
)

type Scheduler struct {
	settings *settings.Manager
	orch     *Orchestrator
	logger   *slog.Logger

	mu            sync.Mutex
	c             *cron.Cron
	cronExpr      string
	reminderExpr  string
	analyticsExpr string
}

func NewScheduler(mgr *settings.Manager, orch *Orchestrator, logger *slog.Logger) *Scheduler {
	if logger == nil {
		logger = slog.Default()
	}
	return &Scheduler{
		settings: mgr,
		orch:     orch,
		logger:   logger,
	}
}

func (s *Scheduler) Start(ctx context.Context) error {
	if err := s.Reload(ctx); err != nil {
		return err
	}

	// Poll every minute so CLI changes to cron schedules take effect without restart.
	go func() {
		ticker := time.NewTicker(1 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				s.Stop()
				return
			case <-ticker.C:
				_ = s.Reload(ctx)
			}
		}
	}()

	return nil
}

func (s *Scheduler) Reload(ctx context.Context) error {
	cronExpr, err := s.settings.GetString(ctx, settings.KeyCronSchedule)
	if err != nil || cronExpr == "" {
		cronExpr = "0 8 * * 2,4"
	}
	reminderExpr, err := s.settings.GetString(ctx, settings.KeyReviewReminderSchedule)
	if err != nil || reminderExpr == "" {
		reminderExpr = "0 10,18 * * *"
	}
	analyticsExpr, err := s.settings.GetString(ctx, settings.KeyAnalyticsReminderSchedule)
	if err != nil || analyticsExpr == "" {
		analyticsExpr = "0 19 * * *"
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.c != nil && s.cronExpr == cronExpr && s.reminderExpr == reminderExpr && s.analyticsExpr == analyticsExpr {
		return nil
	}

	newCron := cron.New()
	if _, err := newCron.AddFunc(cronExpr, func() {
		runCtx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		if err := s.orch.RunScheduled(runCtx); err != nil {
			s.logger.Error("scheduled run failed", "err", err)
		}
	}); err != nil {
		return err
	}

	if _, err := newCron.AddFunc(reminderExpr, func() {
		remCtx, cancel := context.WithTimeout(context.Background(), 1*time.Minute)
		defer cancel()
		if err := s.orch.SendPendingReminders(remCtx); err != nil {
			s.logger.Error("review reminder failed", "err", err)
		}
	}); err != nil {
		return err
	}

	if _, err := newCron.AddFunc(analyticsExpr, func() {
		anCtx, cancel := context.WithTimeout(context.Background(), 1*time.Minute)
		defer cancel()
		if err := s.orch.SendMetricsReminders(anCtx); err != nil {
			s.logger.Error("metrics reminder failed", "err", err)
		}
	}); err != nil {
		return err
	}

	if s.c != nil {
		s.c.Stop()
	}
	newCron.Start()
	s.c = newCron
	s.cronExpr = cronExpr
	s.reminderExpr = reminderExpr
	s.analyticsExpr = analyticsExpr
	s.logger.Info("scheduler loaded", "cron", cronExpr, "reminders", reminderExpr, "analytics", analyticsExpr)
	return nil
}

func (s *Scheduler) CurrentSchedules() (cronExpr string, reminderExpr string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cronExpr, s.reminderExpr
}

func (s *Scheduler) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.c != nil {
		ctx := s.c.Stop()
		<-ctx.Done()
		s.c = nil
	}
}
