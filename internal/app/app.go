package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	identitycommunication "github.com/ranakdinesh/setika/internal/adapters/communication/identity"
	apphandlers "github.com/ranakdinesh/setika/internal/adapters/httpx/handlers"
	identityadapter "github.com/ranakdinesh/setika/internal/adapters/identity"
	"github.com/ranakdinesh/setika/internal/config"
	"github.com/ranakdinesh/setika/internal/infrastructure"
	"github.com/ranakdinesh/setika/internal/logger"
	documentsign "github.com/ranakdinesh/spur-documentsign"
	hrms "github.com/ranakdinesh/spur-hrms"
	hrmspermissions "github.com/ranakdinesh/spur-hrms/pkg/permissions"
	identity "github.com/ranakdinesh/spur-identity"
	"github.com/ranakdinesh/spur-identity/adapters/http/httputil"
	"golang.org/x/crypto/bcrypt"
	// SPUR:IMPORTS:END
)

type App struct {
	Infra *infrastructure.Infra
	// SPUR:APP_VALUES
	Identity     *identity.Module
	DocumentSign *documentsign.Module
	Hrms         *hrms.Module
	// SPUR:APP_VALUES:END
}

func New(ctx context.Context) (*App, error) {
	var cfg config.Config
	if err := config.Load(&cfg); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}

	log := logger.NewWithOptions(logger.Options{
		Environment: cfg.AppEnv,
	})

	infra, err := infrastructure.Bootstrap(ctx, &cfg, log)
	if err != nil {
		return nil, err
	}

	// SPUR:MODULES
	authClientID, err := uuid.Parse(cfg.AuthClientID)
	if err != nil {
		return nil, fmt.Errorf("AUTH_CLIENT_ID: %w", err)
	}
	privateKey, err := config.LoadPrivateKey(cfg.JWTPrivateKeyPath)
	if err != nil {
		return nil, fmt.Errorf("JWT private key: %w", err)
	}
	identityCfg := identity.Config{
		Issuer:            cfg.IdentityIssuer,
		GlobalSecret:      []byte(cfg.FositeGlobalSecret),
		JWTPrivateKeyPath: cfg.JWTPrivateKeyPath,
		AuthClientId:      authClientID,
		AuthClientSecret:  cfg.AuthClientSecret,
		FrontendURL:       cfg.FrontendURL,
		CookieName:        "spur_sso",
		CookieSecure:      cfg.AppEnv == "production",
		BootstrapKey:      cfg.APIKeyValue,
		BootstrapPassword: cfg.IdentityBootstrapPassword,
	}
	identityLog := infra.Log.Logger()
	identityCommunication := identitycommunication.New(cfg, infra.Log)
	identityModule, err := identity.New(ctx, identity.Options{
		DB:            infra.DB,
		Log:           &identityLog,
		Cfg:           identityCfg,
		PrivateKey:    privateKey,
		Redis:         infra.Redis,
		Communication: identityCommunication,
	})
	if err != nil {
		return nil, fmt.Errorf("identity: %w", err)
	}

	if err := identityModule.Services.ModuleService.RegisterManifest(ctx, identityModule.Manifest); err != nil {
		return nil, fmt.Errorf("identity manifest: %w", err)
	}
	documentSignModule, err := documentsign.New(ctx, documentsign.Options{
		DB:                  infra.DB,
		Log:                 &identityLog,
		Cfg:                 documentSignConfigFromAppConfig(cfg),
		TenantIDFromContext: httputil.GetTenantID,
		UserIDFromContext:   httputil.GetUserID,
		IsSuperAdmin:        httputil.IsSuperAdmin,
	})
	if err != nil {
		return nil, fmt.Errorf("document-sign: %w", err)
	}
	if err := identityModule.Services.ModuleService.RegisterManifest(ctx, documentSignModule.Manifest); err != nil {
		return nil, fmt.Errorf("document-sign manifest: %w", err)
	}
	employeeIdentity, err := identityadapter.NewEmployeeIdentityAdapter(identityModule)
	if err != nil {
		return nil, fmt.Errorf("hrms employee identity adapter: %w", err)
	}

	hrmsModule, err := hrms.New(ctx, hrms.Options{
		DB:                     infra.DB,
		Log:                    &identityLog,
		Cfg:                    hrmsConfigFromAppConfig(cfg),
		TenantIDFromContext:    httputil.GetTenantID,
		UserIDFromContext:      httputil.GetUserID,
		IsSuperAdmin:           httputil.IsSuperAdmin,
		RolesFromContext:       httputil.GetRoles,
		PermissionsFromContext: httputil.GetPermissions,
		EmployeeIdentity:       employeeIdentity,
	})
	if err != nil {
		return nil, fmt.Errorf("hrms: %w", err)
	}
	if err := identityModule.Services.ModuleService.RegisterManifest(ctx, hrmsModule.Manifest); err != nil {
		return nil, fmt.Errorf("hrms manifest: %w", err)
	}
	if err := syncHRMSBaselineRoles(ctx, infra.DB); err != nil {
		return nil, fmt.Errorf("hrms baseline role sync: %w", err)
	}

	if err := bootstrapIdentity(ctx, infra.Log, identityModule, cfg); err != nil {
		return nil, fmt.Errorf("identity bootstrap: %w", err)
	}
	// SPUR:MODULES:END

	application := &App{
		Infra: infra,
		// SPUR:APP_RETURN
		Identity:     identityModule,
		DocumentSign: documentSignModule,
		Hrms:         hrmsModule,
		// SPUR:APP_RETURN:END
	}
	httpHandlers := apphandlers.New(apphandlers.Options{
		Identity:         identityModule,
		Hrms:             hrmsModule,
		Communication:    identityCommunication,
		DB:               infra.DB,
		Redis:            infra.Redis,
		Log:              infra.Log,
		PrivateKey:       privateKey,
		IdentityIssuer:   cfg.IdentityIssuer,
		LoginSessionTTL:  cfg.SetikaLoginSessionTTL,
		TenantBaseDomain: cfg.TenantBaseDomain,
		FrontendURL:      cfg.FrontendURL,
		SignupAlertEmail: cfg.SignupAlertEmail,
	})

	infra.HTTP.Mount(func(r chi.Router) {
		r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"status":"ok"}`))
		})
		r.Post("/setika/auth/login", httpHandlers.Login)
		r.Post("/setika/applicants/apply", httpHandlers.ApplyForJob)
		r.Post("/signup", httpHandlers.Signup)
		r.Get("/signup/verify", httpHandlers.VerifySignup)
		r.Get("/signup/subdomain-availability", httpHandlers.SignupSubdomainAvailability)
		r.With(identityModule.AuthMiddleware()).Get("/master-data/countries", httpHandlers.MasterCountries)
		r.With(identityModule.AuthMiddleware()).Get("/master-data/timezones", httpHandlers.MasterTimezones)
		r.With(identityModule.AuthMiddleware()).Get("/admin/tenants", httpHandlers.AdminListTenants)
		r.With(identityModule.AuthMiddleware()).Post("/admin/tenants/provision", httpHandlers.AdminProvisionTenant)
		r.With(identityModule.AuthMiddleware()).Get("/admin/signup-intents", httpHandlers.AdminListSignupIntents)
		r.With(identityModule.AuthMiddleware()).Put("/admin/signup-intents/{intentID}", httpHandlers.AdminUpdateSignupIntent)
		r.With(identityModule.AuthMiddleware()).Delete("/admin/signup-intents/{intentID}", httpHandlers.AdminDeleteSignupIntent)
		r.With(identityModule.AuthMiddleware()).Post("/admin/signup-intents/{intentID}/manual-provision", httpHandlers.AdminManualProvisionSignupIntent)
		r.With(identityModule.AuthMiddleware()).Put("/admin/tenants/{tenantID}/users/{userID}", httpHandlers.AdminUpdateTenantUser)
		// SPUR:ROUTES
		identityModule.RegisterRoutes(r)
		// Override identity resend route so the public flow runs with identity RLS system context.
		r.Post("/auth/email/verification/resend", httpHandlers.ResendEmailVerification)
		hrmsModule.RegisterRoutes(r, identityModule.AuthMiddleware())
		documentSignModule.RegisterRoutes(r, identityModule.AuthMiddleware())
		// SPUR:ROUTES:END
	})

	return application, nil
}

func documentSignConfigFromAppConfig(cfg config.Config) documentsign.Config {
	return documentsign.Config{
		StorageProvider:            cfg.StorageProvider,
		StorageEnabled:             cfg.StorageEnabled,
		StorageBucket:              cfg.StorageBucket,
		StorageRegion:              cfg.StorageRegion,
		StorageEndpoint:            cfg.StorageEndpoint,
		StorageAccessKeyID:         cfg.StorageAccessKeyID,
		StorageSecretAccessKey:     cfg.StorageSecretAccessKey,
		StorageUseSSL:              cfg.StorageUseSSL,
		StorageForcePathStyle:      cfg.StorageForcePathStyle,
		StorageObjectPrefix:        cfg.StorageObjectPrefix,
		StoragePublicBaseURL:       cfg.StoragePublicBaseURL,
		StorageMaxFileSizeBytes:    cfg.StorageMaxFileSizeBytes,
		StorageAllowedContentTypes: cfg.StorageAllowedContentTypes,
	}
}

func hrmsConfigFromAppConfig(cfg config.Config) hrms.Config {
	return hrms.Config{
		EmailProvider:              cfg.EmailProvider,
		EmailFromName:              firstNonEmpty(cfg.EmailFromName, cfg.SMTPFromName),
		EmailFromEmail:             firstNonEmpty(cfg.EmailFromEmail, cfg.SMTPFromEmail),
		EmailReplyToEmail:          cfg.EmailReplyToEmail,
		SMTPHost:                   cfg.SMTPHost,
		SMTPPort:                   int32(cfg.SMTPPort),
		SMTPUsername:               cfg.SMTPUsername,
		SMTPPassword:               cfg.SMTPPassword,
		SMTPEncryption:             cfg.SMTPEncryption,
		SendGridAPIKey:             cfg.SendGridAPIKey,
		SendGridSandboxMode:        cfg.SendGridSandboxMode,
		EmailWebhookSigningSecret:  cfg.EmailWebhookSigningSecret,
		StorageProvider:            cfg.StorageProvider,
		StorageEnabled:             cfg.StorageEnabled,
		StorageBucket:              cfg.StorageBucket,
		StorageRegion:              cfg.StorageRegion,
		StorageEndpoint:            cfg.StorageEndpoint,
		StorageAccessKeyID:         cfg.StorageAccessKeyID,
		StorageSecretAccessKey:     cfg.StorageSecretAccessKey,
		StorageUseSSL:              cfg.StorageUseSSL,
		StorageForcePathStyle:      cfg.StorageForcePathStyle,
		StorageObjectPrefix:        cfg.StorageObjectPrefix,
		StoragePublicBaseURL:       cfg.StoragePublicBaseURL,
		StorageMaxFileSizeBytes:    cfg.StorageMaxFileSizeBytes,
		StorageAllowedContentTypes: cfg.StorageAllowedContentTypes,
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}

func syncHRMSBaselineRoles(ctx context.Context, db interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}) error {
	employeePermissions := hrmspermissions.ManifestRolePermissions("EMPLOYEE")
	managerPermissions := hrmspermissions.ManifestRolePermissions("MANAGER")
	applicantPermissions := hrmspermissions.ManifestRolePermissions("APPLICANT")
	if len(employeePermissions) == 0 || len(managerPermissions) == 0 || len(applicantPermissions) == 0 {
		return fmt.Errorf("hrms employee/manager/applicant role permissions are not declared")
	}

	if _, err := db.Exec(ctx, `
		INSERT INTO auth.tenant_modules (tenant_id, module_id, status, access_source)
		SELECT t.id, m.id, 'active', 'admin_grant'
		FROM auth.tenants t
		CROSS JOIN auth.modules m
		WHERE t.kind <> 'ops'
		  AND m.code = 'hrms'
		ON CONFLICT (tenant_id, module_id) DO UPDATE
		SET status = 'active',
			access_source = EXCLUDED.access_source,
			updated_at = NOW()
	`); err != nil {
		return fmt.Errorf("enable hrms for tenants: %w", err)
	}

	if _, err := db.Exec(ctx, `
		INSERT INTO auth.roles (id, tenant_id, name, code, description, is_system)
		SELECT gen_random_uuid(), t.id, 'Employee', 'EMPLOYEE',
			'Baseline self-service role for every tenant user.', TRUE
		FROM auth.tenants t
		WHERE t.kind <> 'ops'
		  AND NOT EXISTS (
			SELECT 1 FROM auth.roles r
			WHERE r.tenant_id = t.id
			  AND (UPPER(COALESCE(r.code, '')) = 'EMPLOYEE' OR r.name = 'Employee')
		  )
	`); err != nil {
		return fmt.Errorf("ensure employee roles: %w", err)
	}

	if _, err := db.Exec(ctx, `
		UPDATE auth.roles
		SET code = 'EMPLOYEE',
			name = 'Employee',
			description = 'Baseline self-service role for every tenant user.',
			is_system = TRUE
		WHERE tenant_id IN (SELECT id FROM auth.tenants WHERE kind <> 'ops')
		  AND (UPPER(COALESCE(code, '')) = 'EMPLOYEE' OR name = 'Employee')
	`); err != nil {
		return fmt.Errorf("normalize employee roles: %w", err)
	}

	if _, err := db.Exec(ctx, `
		UPDATE auth.roles
		SET code = 'MANAGER',
			name = 'Manager',
			description = 'Team visibility, leave approvals, and attendance review permissions on top of Employee.',
			is_system = TRUE
		WHERE tenant_id IN (SELECT id FROM auth.tenants WHERE kind <> 'ops')
		  AND (UPPER(COALESCE(code, '')) = 'MANAGER' OR name = 'Manager')
	`); err != nil {
		return fmt.Errorf("normalize manager roles: %w", err)
	}

	if _, err := db.Exec(ctx, `
		INSERT INTO auth.roles (id, tenant_id, name, code, description, is_system)
		SELECT gen_random_uuid(), t.id, 'Applicant', 'APPLICANT',
			'External candidate access for own applicant profile and application status only.', TRUE
		FROM auth.tenants t
		WHERE t.kind <> 'ops'
		  AND NOT EXISTS (
			SELECT 1 FROM auth.roles r
			WHERE r.tenant_id = t.id
			  AND (UPPER(COALESCE(r.code, '')) = 'APPLICANT' OR r.name = 'Applicant')
		  )
	`); err != nil {
		return fmt.Errorf("ensure applicant roles: %w", err)
	}

	if _, err := db.Exec(ctx, `
		UPDATE auth.roles
		SET code = 'APPLICANT',
			name = 'Applicant',
			description = 'External candidate access for own applicant profile and application status only.',
			is_system = TRUE
		WHERE tenant_id IN (SELECT id FROM auth.tenants WHERE kind <> 'ops')
		  AND (UPPER(COALESCE(code, '')) = 'APPLICANT' OR name = 'Applicant')
	`); err != nil {
		return fmt.Errorf("normalize applicant roles: %w", err)
	}

	if _, err := db.Exec(ctx, `
		DELETE FROM auth.role_permissions rp
		USING auth.roles r, auth.permissions p
		WHERE rp.role_id = r.id
		  AND rp.permission_id = p.id
		  AND p.module = 'hrms'
		  AND (
			(r.code = 'EMPLOYEE' AND NOT (p.key = ANY($1::text[])))
			OR (r.code = 'MANAGER' AND NOT (p.key = ANY($2::text[])))
			OR (r.code = 'APPLICANT' AND NOT (p.key = ANY($3::text[])))
		  )
	`, employeePermissions, managerPermissions, applicantPermissions); err != nil {
		return fmt.Errorf("prune hrms baseline role permissions: %w", err)
	}

	if _, err := db.Exec(ctx, `
		INSERT INTO auth.role_permissions (role_id, permission_id)
		SELECT r.id, p.id
		FROM auth.roles r
		JOIN auth.permissions p ON p.module = 'hrms'
		WHERE (r.code = 'EMPLOYEE' AND p.key = ANY($1::text[]))
		   OR (r.code = 'MANAGER' AND p.key = ANY($2::text[]))
		   OR (r.code = 'APPLICANT' AND p.key = ANY($3::text[]))
		ON CONFLICT DO NOTHING
	`, employeePermissions, managerPermissions, applicantPermissions); err != nil {
		return fmt.Errorf("assign hrms baseline role permissions: %w", err)
	}

	if _, err := db.Exec(ctx, `
		INSERT INTO auth.user_roles (user_id, role_id)
		SELECT u.id, r.id
		FROM auth.users u
		JOIN auth.roles r ON r.tenant_id = u.tenant_id AND r.code = 'EMPLOYEE'
		WHERE u.is_super_admin = FALSE
		  AND NOT EXISTS (
			SELECT 1
			FROM auth.user_roles existing_ur
			JOIN auth.roles existing_r ON existing_r.id = existing_ur.role_id
			WHERE existing_ur.user_id = u.id
			  AND existing_r.tenant_id = u.tenant_id
			  AND existing_r.code = 'APPLICANT'
		  )
		ON CONFLICT DO NOTHING
	`); err != nil {
		return fmt.Errorf("assign employee role to tenant users: %w", err)
	}

	return nil
}

func (a *App) Start(ctx context.Context) error {
	return a.Infra.HTTP.Start(ctx)
}

func bootstrapIdentity(ctx context.Context, log *logger.Loggerx, identityModule *identity.Module, cfg config.Config) error {
	if identityModule == nil || identityModule.DB == nil {
		return fmt.Errorf("identity module not initialized")
	}

	tx, err := identityModule.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	var tenantCount int
	if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM auth.tenants`).Scan(&tenantCount); err != nil {
		return err
	}
	if tenantCount > 0 {
		log.Info(ctx).Msg("identity bootstrap skipped: existing tenants found")
		return nil
	}

	email := strings.TrimSpace(strings.ToLower(cfg.IdentityBootstrapEmail))
	if email == "" {
		email = "superadmin@sysops.local"
	}
	firstName := strings.TrimSpace(cfg.IdentityBootstrapFirstName)
	if firstName == "" {
		firstName = "Super"
	}
	lastName := strings.TrimSpace(cfg.IdentityBootstrapLastName)
	if lastName == "" {
		lastName = "Admin"
	}

	password := cfg.IdentityBootstrapPassword
	generatedPassword := false
	if password == "" {
		b := make([]byte, 16)
		if _, err := rand.Read(b); err != nil {
			return err
		}
		password = hex.EncodeToString(b)
		generatedPassword = true
	}
	passwordBytes, err := bcrypt.GenerateFromPassword([]byte(password), 14)
	if err != nil {
		return err
	}

	tenantID := uuid.New()
	userID := uuid.New()
	roleID := uuid.New()

	if _, err := tx.Exec(ctx, `
		INSERT INTO auth.tenants (id, name, kind)
		VALUES ($1, $2, 'ops')
	`, tenantID, "SysOps"); err != nil {
		return err
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO auth.users (
			id, tenant_id, first_name, last_name, email, password_hash,
			is_super_admin, authz_version, is_active,
			email_verified_at, verified_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, TRUE, 1, TRUE, NOW(), NOW())
	`, userID, tenantID, firstName, lastName, email, string(passwordBytes)); err != nil {
		return err
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO auth.roles (id, tenant_id, name, code, description, is_system)
		VALUES ($1, $2, 'Super Admin', 'SUPER_ADMIN', 'Root access role', TRUE)
	`, roleID, tenantID); err != nil {
		return err
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO auth.role_permissions (role_id, permission_id)
		SELECT $1, id FROM auth.permissions
		ON CONFLICT DO NOTHING
	`, roleID); err != nil {
		return err
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO auth.user_roles (user_id, role_id)
		VALUES ($1, $2)
	`, userID, roleID); err != nil {
		return err
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO auth.user_profiles (user_id, tenant_id, display_name, default_dashboard_module)
		VALUES ($1, $2, $3, 'identity')
		ON CONFLICT (user_id) DO NOTHING
	`, userID, tenantID, strings.TrimSpace(firstName+" "+lastName)); err != nil {
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return err
	}

	pwHint := "password configured by IDENTITY_BOOTSTRAP_PASSWORD"
	if generatedPassword {
		pwHint = "password=" + password
	}
	log.Info(ctx).
		Str("tenant", "ops").
		Str("email", email).
		Msg("identity bootstrap created ops superadmin user: " + pwHint)
	return nil
}
