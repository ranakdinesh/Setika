package infrastructure

import (
	"context"
	"net"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/ranakdinesh/setika/internal/config"
	"github.com/ranakdinesh/setika/internal/logger"
)

var tenantSubdomainPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

var reservedTenantSubdomains = map[string]struct{}{
	"admin":    {},
	"api":      {},
	"app":      {},
	"auth":     {},
	"billing":  {},
	"dev":      {},
	"files":    {},
	"identity": {},
	"mail":     {},
	"staging":  {},
	"support":  {},
	"www":      {},
}

func NewTenantOriginValidator(pool *pgxpool.Pool, cfg *config.Config, log *logger.Loggerx) func(context.Context, string) bool {
	baseDomain := ""
	if cfg != nil {
		baseDomain = strings.Trim(strings.ToLower(cfg.TenantBaseDomain), ". ")
	}
	if pool == nil || baseDomain == "" {
		return nil
	}

	return func(ctx context.Context, origin string) bool {
		subdomain, ok := tenantSubdomainFromOrigin(origin, baseDomain)
		if !ok {
			return false
		}

		queryCtx, cancel := context.WithTimeout(ctx, 750*time.Millisecond)
		defer cancel()

		tx, err := pool.Begin(queryCtx)
		if err != nil {
			logTenantOriginError(log, queryCtx, "begin tenant origin check", err, subdomain)
			return false
		}
		defer tx.Rollback(queryCtx)

		if _, err := tx.Exec(queryCtx, "SELECT set_config('app.tenant_id', '', true), set_config('app.is_super_admin', 'true', true)"); err != nil {
			logTenantOriginError(log, queryCtx, "set tenant origin system context", err, subdomain)
			return false
		}

		var exists bool
		if err := tx.QueryRow(queryCtx, "SELECT EXISTS (SELECT 1 FROM hrms.tenant_profiles WHERE subdomain = $1)", subdomain).Scan(&exists); err != nil {
			logTenantOriginError(log, queryCtx, "query tenant origin", err, subdomain)
			return false
		}
		return exists
	}
}

func tenantSubdomainFromOrigin(origin string, baseDomain string) (string, bool) {
	parsed, err := url.Parse(strings.TrimSpace(origin))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", false
	}
	if parsed.Scheme != "https" && parsed.Scheme != "http" {
		return "", false
	}

	host := strings.ToLower(parsed.Host)
	if splitHost, _, err := net.SplitHostPort(host); err == nil {
		host = splitHost
	}
	host = strings.Trim(host, ".")
	if parsed.Scheme != "https" && host != "localhost" && !strings.HasPrefix(host, "127.") {
		return "", false
	}
	if host == baseDomain || host == "www."+baseDomain {
		return "", false
	}
	suffix := "." + baseDomain
	if !strings.HasSuffix(host, suffix) {
		return "", false
	}

	subdomain := strings.TrimSuffix(host, suffix)
	if strings.Contains(subdomain, ".") || !tenantSubdomainPattern.MatchString(subdomain) {
		return "", false
	}
	if _, reserved := reservedTenantSubdomains[subdomain]; reserved {
		return "", false
	}
	return subdomain, true
}

func logTenantOriginError(log *logger.Loggerx, ctx context.Context, operation string, err error, subdomain string) {
	if log == nil || err == nil {
		return
	}
	log.Warn(ctx).Err(err).Str("operation", operation).Str("subdomain", subdomain).Msg("tenant origin validation failed")
}
