package handlers

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

type signupRateLimitSubject struct {
	IP        string
	Email     string
	Mobile    string
	Subdomain string
}

type signupRateLimitRule struct {
	Name   string
	Value  string
	Limit  int64
	Window time.Duration
}

var errSignupRateLimited = errors.New("too many signup attempts; please try again later")

func (h *Handler) enforceSignupRateLimits(ctx context.Context, subject signupRateLimitSubject) error {
	rules := []signupRateLimitRule{
		{Name: "ip_hour", Value: subject.IP, Limit: 5, Window: time.Hour},
		{Name: "email_hour", Value: subject.Email, Limit: 3, Window: time.Hour},
		{Name: "mobile_hour", Value: subject.Mobile, Limit: 3, Window: time.Hour},
		{Name: "workspace_hour", Value: subject.Subdomain, Limit: 3, Window: time.Hour},
		{Name: "ip_day", Value: subject.IP, Limit: 20, Window: 24 * time.Hour},
	}
	for _, rule := range rules {
		if strings.TrimSpace(rule.Value) == "" {
			continue
		}
		limited, err := h.signupRateLimitExceeded(ctx, rule)
		if err != nil {
			h.logSignupError(ctx, "signup rate limit check", err)
			return errors.New("signup protection is temporarily unavailable")
		}
		if limited {
			return errSignupRateLimited
		}
	}
	return nil
}

func (h *Handler) signupRateLimitExceeded(ctx context.Context, rule signupRateLimitRule) (bool, error) {
	if h != nil && h.redis != nil {
		limited, err := h.signupRateLimitRedis(ctx, rule)
		if err == nil {
			return limited, nil
		}
		if h.log != nil && !errors.Is(err, redis.Nil) {
			h.log.Warn(ctx).Err(err).Str("bucket", rule.Name).Msg("signup redis rate limit failed; falling back to db")
		}
	}
	return h.signupRateLimitDB(ctx, rule)
}

func (h *Handler) signupRateLimitRedis(ctx context.Context, rule signupRateLimitRule) (bool, error) {
	key := signupRateLimitKey(rule)
	count, err := h.redis.Incr(ctx, key).Result()
	if err != nil {
		return false, err
	}
	if count == 1 {
		if err := h.redis.Expire(ctx, key, rule.Window).Err(); err != nil {
			return false, err
		}
	}
	return count > rule.Limit, nil
}

func (h *Handler) signupRateLimitDB(ctx context.Context, rule signupRateLimitRule) (bool, error) {
	if h == nil || h.db == nil {
		return false, errors.New("rate limit database is not configured")
	}
	var attempts int64
	if err := h.db.QueryRow(ctx, `
		INSERT INTO platform.signup_rate_limits (bucket_key, attempts, window_start, expires_at, updated_at)
		VALUES ($1, 1, NOW(), NOW() + ($2::int * INTERVAL '1 second'), NOW())
		ON CONFLICT (bucket_key) DO UPDATE
		SET attempts = CASE
				WHEN platform.signup_rate_limits.window_start + ($2::int * INTERVAL '1 second') <= NOW() THEN 1
				ELSE platform.signup_rate_limits.attempts + 1
			END,
			window_start = CASE
				WHEN platform.signup_rate_limits.window_start + ($2::int * INTERVAL '1 second') <= NOW() THEN NOW()
				ELSE platform.signup_rate_limits.window_start
			END,
			expires_at = CASE
				WHEN platform.signup_rate_limits.window_start + ($2::int * INTERVAL '1 second') <= NOW() THEN NOW() + ($2::int * INTERVAL '1 second')
				ELSE platform.signup_rate_limits.expires_at
			END,
			updated_at = NOW()
		RETURNING attempts
	`, signupRateLimitKey(rule), int(rule.Window.Seconds())).Scan(&attempts); err != nil {
		return false, err
	}
	return attempts > rule.Limit, nil
}

func signupRateLimitKey(rule signupRateLimitRule) string {
	return fmt.Sprintf("setika:signup:%s:%s", rule.Name, strings.ToLower(strings.TrimSpace(rule.Value)))
}

func clientIPAddress(r *http.Request) string {
	if r == nil {
		return "unknown"
	}
	for _, header := range []string{"CF-Connecting-IP", "X-Real-IP"} {
		value := strings.TrimSpace(r.Header.Get(header))
		if value != "" {
			return value
		}
	}
	if forwarded := strings.TrimSpace(r.Header.Get("X-Forwarded-For")); forwarded != "" {
		parts := strings.Split(forwarded, ",")
		if value := strings.TrimSpace(parts[0]); value != "" {
			return value
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil && strings.TrimSpace(host) != "" {
		return host
	}
	if strings.TrimSpace(r.RemoteAddr) != "" {
		return strings.TrimSpace(r.RemoteAddr)
	}
	return "unknown"
}
