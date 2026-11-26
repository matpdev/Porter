package linkedin

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"codeberg.org/matheus/porter/internal/store"
)

// EvaluateTokenAlert checks whether the LinkedIn OAuth2 token needs an expiration warning.
// Alerts are throttled to at most once every 24h unless the token is already expired.
func EvaluateTokenAlert(tok *store.OAuthToken, now time.Time) (bool, string) {
	if tok == nil {
		return false, ""
	}

	remaining := tok.ExpiresAt.Sub(now)
	daysLeft := int(math.Ceil(remaining.Hours() / 24.0))

	if tok.LastAlertSentAt != nil && now.Sub(*tok.LastAlertSentAt) < 24*time.Hour && remaining > 0 {
		return false, ""
	}

	switch {
	case remaining <= 0:
		return true, "🚨 LinkedIn OAuth2 token has EXPIRED! Run /auth (or `porter auth linkedin`) to reconnect."
	case daysLeft <= 1:
		return true, "🔴 LinkedIn OAuth2 token expires in less than 1 day! Run /auth to renew it."
	case daysLeft <= 3:
		return true, fmt.Sprintf("🟠 LinkedIn OAuth2 token expires in %d days (%s). Run /auth soon.", daysLeft, tok.ExpiresAt.Format("2006-01-02"))
	case daysLeft <= 7:
		return true, fmt.Sprintf("🟡 LinkedIn OAuth2 token expires in %d days (%s). Run /auth when convenient.", daysLeft, tok.ExpiresAt.Format("2006-01-02"))
	default:
		return false, ""
	}
}

type TokenMonitor struct {
	repo *store.Repository
}

func NewTokenMonitor(repo *store.Repository) *TokenMonitor {
	return &TokenMonitor{repo: repo}
}

func (m *TokenMonitor) CheckAndNotify(ctx context.Context, notify func(context.Context, string) error) error {
	tok, err := m.repo.GetOAuthToken(ctx, ProviderLinkedIn)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}

	shouldAlert, msg := EvaluateTokenAlert(tok, time.Now().UTC())
	if !shouldAlert {
		return nil
	}
	if err := notify(ctx, msg); err != nil {
		return err
	}
	return m.repo.TouchOAuthTokenAlert(ctx, ProviderLinkedIn)
}
