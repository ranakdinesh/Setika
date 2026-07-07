package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"reflect"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/ranakdinesh/setika/internal/app"
	"github.com/ranakdinesh/setika/internal/config"
	hrmspostgres "github.com/ranakdinesh/spur-hrms/adapters/postgres"
	hrmsdomain "github.com/ranakdinesh/spur-hrms/core/domain"
	hrmsports "github.com/ranakdinesh/spur-hrms/core/ports"
	"github.com/ranakdinesh/spur-identity/adapters/http/httputil"
	identitypostgres "github.com/ranakdinesh/spur-identity/adapters/postgres"
)

const demoSource = "demo_seed"

func main() {
	ctx := context.Background()
	if err := run(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "demo seed refused or failed: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	var cfg config.Config
	if err := config.Load(&cfg); err != nil {
		return err
	}
	password, generated, err := validateGuards(ctx, cfg)
	if err != nil {
		return err
	}

	application, err := app.New(ctx)
	if err != nil {
		return err
	}
	if application.Infra != nil && application.Infra.DB != nil {
		defer application.Infra.DB.Close()
	}

	log := application.Infra.Log.Logger()
	seeder := &demoSeeder{
		app:           application,
		identity:      newIdentityFacade(application),
		identityStore: identitypostgres.NewStore(application.Identity.DB),
		hrmsStore:     hrmspostgres.New(application.Infra.DB, &log),
		password:      password,
	}

	platformSummary, err := seeder.seedPlatformRoles(ctx)
	if err != nil {
		return err
	}

	summaries := make([]tenantSummary, 0, len(demoTenants()))
	for _, tenant := range demoTenants() {
		summary, err := seeder.seedTenant(ctx, tenant)
		if err != nil {
			return err
		}
		summaries = append(summaries, summary)
	}

	fmt.Println("Setika guarded demo seed completed.")
	if generated {
		fmt.Printf("Generated local-only demo password: %s\n", password)
	}
	fmt.Println()
	for _, summary := range summaries {
		fmt.Printf("- %s (%s): tenant=%s plan=%s users created=%d reused=%d employees created=%d reused=%d\n",
			summary.Name, summary.Subdomain, summary.TenantID, summary.PlanCode, summary.UsersCreated, summary.UsersReused, summary.EmployeesCreated, summary.EmployeesReused)
	}
	fmt.Println()
	fmt.Printf("Platform roles: created=%d updated=%d reused=%d permissions assigned=%d missing recommended=%d ops tenant=%s\n",
		platformSummary.Created, platformSummary.Updated, platformSummary.Reused, platformSummary.PermissionsAssigned, platformSummary.MissingRecommended, platformSummary.OpsTenantID)
	fmt.Println()
	fmt.Println("Demo credentials:")
	for _, tenant := range demoTenants() {
		for _, user := range tenant.Credentials {
			fmt.Printf("- %s / %s\n", user, password)
		}
	}
	fmt.Println()
	fmt.Println("Deferred: richer leave, attendance, payroll, hiring, onboarding, and document samples are TODOs in the command and stabilization/19-demo-seed-followup.md.")
	return nil
}

func validateGuards(ctx context.Context, cfg config.Config) (string, bool, error) {
	if strings.TrimSpace(os.Getenv("SETIKA_DEMO_SEED")) != "1" {
		return "", false, errors.New("SETIKA_DEMO_SEED=1 is required")
	}
	appEnv := strings.ToLower(strings.TrimSpace(cfg.AppEnv))
	if appEnv == "production" || appEnv == "prod" {
		return "", false, errors.New("APP_ENV must not be production")
	}
	allowNonLocal := strings.TrimSpace(os.Getenv("SETIKA_ALLOW_NONLOCAL_DEMO_SEED")) == "1"
	if productionLikeTarget(cfg, allowNonLocal) {
		return "", false, errors.New("target appears production-like; set SETIKA_ALLOW_NONLOCAL_DEMO_SEED=1 only for an approved non-production target")
	}
	password := strings.TrimSpace(os.Getenv("SETIKA_DEMO_PASSWORD"))
	generated := false
	if password == "" {
		var err error
		password, err = generatePassword()
		if err != nil {
			return "", false, err
		}
		generated = true
	}
	if err := requireExistingOpsTenant(ctx, cfg.DatabaseURL); err != nil {
		return "", false, err
	}
	return password, generated, nil
}

func productionLikeTarget(cfg config.Config, allowNonLocal bool) bool {
	dbURL := strings.TrimSpace(cfg.DatabaseURL)
	if dbURL == "" {
		return true
	}
	lower := strings.ToLower(dbURL + " " + cfg.TenantBaseDomain + " " + cfg.IdentityIssuer + " " + cfg.FrontendURL)
	if strings.Contains(lower, "prod") || strings.Contains(lower, "production") || strings.Contains(lower, "live") {
		return true
	}
	if allowNonLocal {
		return false
	}
	u, err := url.Parse(dbURL)
	if err != nil {
		return true
	}
	host := u.Hostname()
	if host == "" {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return false
	}
	ip := net.ParseIP(host)
	return ip == nil || !(ip.IsLoopback() || ip.IsPrivate())
}

func requireExistingOpsTenant(ctx context.Context, databaseURL string) error {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return fmt.Errorf("connect for ops precheck: %w", err)
	}
	defer pool.Close()
	var exists bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM auth.tenants WHERE kind = 'ops')`).Scan(&exists); err != nil {
		return fmt.Errorf("ops tenant precheck failed; run normal platform bootstrap/migrations first: %w", err)
	}
	if !exists {
		return errors.New("ops tenant is missing; refusing to let demo seed create or modify SysOps")
	}
	return nil
}

func generatePassword() (string, error) {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "local-demo-" + hex.EncodeToString(b), nil
}

type demoSeeder struct {
	app           *app.App
	identity      *identityFacade
	identityStore *identitypostgres.Store
	hrmsStore     *hrmspostgres.Store
	password      string
}

type platformRoleSummary struct {
	OpsTenantID           uuid.UUID
	Created               int
	Updated               int
	Reused                int
	PermissionsAssigned   int
	MissingRecommended    int
	SkippedPermissionKeys []string
}

type tenantSummary struct {
	Name             string
	Subdomain        string
	TenantID         uuid.UUID
	PlanCode         string
	UsersCreated     int
	UsersReused      int
	EmployeesCreated int
	EmployeesReused  int
}

func (s *demoSeeder) seedPlatformRoles(ctx context.Context) (platformRoleSummary, error) {
	ctx = httputil.SetSuperAdmin(ctx, true)
	summary := platformRoleSummary{}
	opsTenant, err := s.identity.findOpsTenant(ctx)
	if err != nil {
		return summary, err
	}
	summary.OpsTenantID = opsTenant.ID
	if err := s.identity.ensureModuleEnabled(ctx, opsTenant.ID, "identity"); err != nil {
		return summary, err
	}

	permissions, err := s.identity.listPermissionsByFullKey(ctx)
	if err != nil {
		return summary, err
	}
	for _, seed := range platformRoles() {
		roleID, created, updated, err := s.identity.ensurePlatformRole(ctx, opsTenant.ID, seed)
		if err != nil {
			return summary, err
		}
		switch {
		case created:
			summary.Created++
		case updated:
			summary.Updated++
		default:
			summary.Reused++
		}
		for _, key := range seed.SafePermissionKeys {
			permissionID, ok := permissions[key]
			if !ok {
				summary.SkippedPermissionKeys = append(summary.SkippedPermissionKeys, seed.Code+":"+key)
				continue
			}
			if err := s.identity.assignPermissionToRole(ctx, roleID, permissionID); err != nil {
				summary.SkippedPermissionKeys = append(summary.SkippedPermissionKeys, seed.Code+":"+key)
				continue
			}
			summary.PermissionsAssigned++
		}
		for _, key := range seed.RecommendedPermissionKeys {
			if _, ok := permissions[key]; !ok {
				summary.MissingRecommended++
			}
		}
	}
	return summary, nil
}

func (s *demoSeeder) seedTenant(ctx context.Context, seed demoTenant) (tenantSummary, error) {
	ctx = httputil.SetSuperAdmin(ctx, true)
	summary := tenantSummary{Name: seed.Name, Subdomain: seed.Subdomain, PlanCode: seed.PlanCode}

	tenant, adminUser, created, err := s.ensureTenantAndAdmin(ctx, seed)
	if err != nil {
		return summary, err
	}
	summary.TenantID = tenant.ID
	if created {
		summary.UsersCreated++
	} else {
		summary.UsersReused++
	}

	if err := s.app.Hrms.Services.Hrms.RunAsSystem(ctx, func(systemCtx context.Context) error {
		return s.provisionTenant(systemCtx, seed, tenant.ID)
	}); err != nil {
		return summary, err
	}
	if err := s.ensureSubscription(ctx, tenant, seed.Name, seed.PlanCode, seed.MaxEmployees); err != nil {
		return summary, err
	}
	if err := s.identity.ensureHRMSModuleEnabled(ctx, tenant.ID); err != nil {
		return summary, err
	}
	for _, code := range []string{"TENANT_ADMIN", "EMPLOYEE", "APPLICANT", "HR", "MANAGER"} {
		if _, err := s.identity.ensureRole(ctx, tenant.ID, code); err != nil {
			return summary, err
		}
	}
	if err := s.identity.assignRoleCodes(ctx, adminUser.ID, tenant.ID, []string{"TENANT_ADMIN", "EMPLOYEE"}); err != nil {
		return summary, err
	}

	org, err := s.ensureOrgSetup(ctx, tenant.ID, seed)
	if err != nil {
		return summary, err
	}
	if created, err := s.ensureEmployeeForUser(ctx, tenant.ID, adminUser, seed.Admin.EmployeeCode, seed.Admin, org, uuid.Nil); err != nil {
		return summary, err
	} else if created {
		summary.EmployeesCreated++
	} else {
		summary.EmployeesReused++
	}

	managerUserIDs := map[string]uuid.UUID{}
	managerUserIDs[strings.ToLower(seed.Admin.Email)] = adminUser.ID
	for _, person := range seed.People {
		managerID := uuid.Nil
		if person.ManagerEmail != "" {
			managerID = managerUserIDs[strings.ToLower(person.ManagerEmail)]
		}
		user, userCreated, err := s.ensureEmployeeIdentity(ctx, tenant.ID, person)
		if err != nil {
			return summary, err
		}
		if userCreated {
			summary.UsersCreated++
		} else {
			summary.UsersReused++
		}
		employeeCreated, err := s.ensureEmployeeForUser(ctx, tenant.ID, user, person.EmployeeCode, person, org, managerID)
		if err != nil {
			return summary, err
		}
		if employeeCreated {
			summary.EmployeesCreated++
		} else {
			summary.EmployeesReused++
		}
		managerUserIDs[strings.ToLower(person.Email)] = user.ID
	}

	if seed.Applicant != nil {
		user, userCreated, err := s.ensureDemoUser(ctx, tenant.ID, *seed.Applicant, []string{"APPLICANT"})
		if err != nil {
			return summary, err
		}
		if userCreated {
			summary.UsersCreated++
		} else {
			summary.UsersReused++
		}
		if err := s.app.Hrms.Services.Hrms.RunAsSystem(ctx, func(systemCtx context.Context) error {
			return s.ensureApplicantPortalLink(systemCtx, tenant.ID, user, *seed.Applicant)
		}); err != nil {
			return summary, err
		}
	}

	// TODO: Seed deterministic leave, attendance, payroll, hiring, onboarding,
	// and document samples after validating each service path and natural key.
	return summary, nil
}

func (s *demoSeeder) ensureTenantAndAdmin(ctx context.Context, seed demoTenant) (identityTenant, identityUser, bool, error) {
	if tenant, ok, err := s.findTenantBySubdomain(ctx, seed.Subdomain); err != nil {
		return identityTenant{}, identityUser{}, false, err
	} else if ok {
		user, created, err := s.ensureAdminUser(ctx, tenant.ID, seed.Admin)
		return tenant, user, created, err
	}
	if tenant, ok, err := s.identity.findTenantByName(ctx, seed.Name); err != nil {
		return identityTenant{}, identityUser{}, false, err
	} else if ok {
		user, created, err := s.ensureAdminUser(ctx, tenant.ID, seed.Admin)
		return tenant, user, created, err
	}

	result, err := s.identity.registerTenant(ctx, seed.Name, seed.Admin, s.password)
	if err != nil {
		return identityTenant{}, identityUser{}, false, err
	}
	now := time.Now().UTC()
	if err := s.identityStore.MarkEmailVerified(ctx, result.User.ID, result.Tenant.ID, now); err != nil {
		return identityTenant{}, identityUser{}, false, fmt.Errorf("verify tenant admin %s: %w", seed.Admin.Email, err)
	}
	if err := s.identity.activateUser(ctx, result.User.ID, result.Tenant.ID); err != nil {
		return identityTenant{}, identityUser{}, false, err
	}
	return result.Tenant, result.User, true, nil
}

func (s *demoSeeder) findTenantBySubdomain(ctx context.Context, subdomain string) (identityTenant, bool, error) {
	profiles, err := s.hrmsStore.ListTenantProfiles(ctx)
	if err != nil {
		return identityTenant{}, false, err
	}
	var tenantID uuid.UUID
	for _, profile := range profiles {
		if profile != nil && strings.EqualFold(profile.Subdomain, subdomain) {
			tenantID = profile.TenantID
			break
		}
	}
	if tenantID == uuid.Nil {
		return identityTenant{}, false, nil
	}
	tenant, ok, err := s.identity.findTenantByID(ctx, tenantID)
	if err != nil || !ok {
		return tenant, ok, err
	}
	if tenant.Kind == "ops" {
		return identityTenant{}, false, fmt.Errorf("subdomain %s resolved to ops tenant; refusing", subdomain)
	}
	return tenant, true, nil
}

func (s *demoSeeder) ensureAdminUser(ctx context.Context, tenantID uuid.UUID, admin demoPerson) (identityUser, bool, error) {
	user, created, err := s.ensureDemoUser(ctx, tenantID, admin, []string{"EMPLOYEE"})
	if err != nil {
		return identityUser{}, false, err
	}
	if err := s.identity.assignRoleCodes(ctx, user.ID, tenantID, []string{"TENANT_ADMIN"}); err != nil {
		return identityUser{}, false, err
	}
	return user, created, nil
}

func (s *demoSeeder) ensureEmployeeIdentity(ctx context.Context, tenantID uuid.UUID, person demoPerson) (identityUser, bool, error) {
	roleCodes := []string{"EMPLOYEE"}
	switch person.Role {
	case hrmsdomain.RoleHR:
		roleCodes = []string{"EMPLOYEE", "HR"}
	case hrmsdomain.RoleManager:
		roleCodes = []string{"EMPLOYEE", "MANAGER"}
	}
	return s.ensureDemoUser(ctx, tenantID, person, roleCodes)
}

func (s *demoSeeder) ensureDemoUser(ctx context.Context, tenantID uuid.UUID, person demoPerson, roleCodes []string) (identityUser, bool, error) {
	user, ok, err := s.identity.findUserByEmail(ctx, person.Email)
	if err != nil {
		return identityUser{}, false, err
	}
	if ok {
		if user.TenantID != tenantID {
			return identityUser{}, false, fmt.Errorf("demo email %s already belongs to tenant %s", person.Email, user.TenantID)
		}
		if err := s.identity.updateUserPassword(ctx, user.ID, tenantID, s.password); err != nil {
			return identityUser{}, false, err
		}
		if err := s.identity.activateUser(ctx, user.ID, tenantID); err != nil {
			return identityUser{}, false, err
		}
		if err := s.identityStore.MarkEmailVerified(ctx, user.ID, tenantID, time.Now().UTC()); err != nil {
			return identityUser{}, false, err
		}
		if err := s.identity.assignRoleCodes(ctx, user.ID, tenantID, roleCodes); err != nil {
			return identityUser{}, false, err
		}
		return user, false, nil
	}
	for _, code := range roleCodes {
		if _, err := s.identity.ensureRole(ctx, tenantID, code); err != nil {
			return identityUser{}, false, err
		}
	}
	created, err := s.identity.createUser(ctx, tenantID, person, s.password, roleCodes)
	if err != nil {
		return identityUser{}, false, err
	}
	return created, true, nil
}

func (s *demoSeeder) provisionTenant(ctx context.Context, seed demoTenant, tenantID uuid.UUID) error {
	adminEmail := seed.Admin.Email
	adminName := seed.Admin.FirstName + " " + seed.Admin.LastName
	tenantURL := "http://" + seed.Subdomain + ".setika.com"
	if _, err := s.app.Hrms.Services.Hrms.ProvisionTenant(ctx, hrmsports.ProvisionTenantCommand{
		TenantID:             tenantID,
		CompanyName:          seed.Name,
		Subdomain:            seed.Subdomain,
		MobileActivationCode: strings.ToUpper(seed.Subdomain) + "-DEMO",
		AdminEmail:           &adminEmail,
		AdminName:            &adminName,
		TenantURL:            &tenantURL,
	}); err != nil {
		return err
	}
	displayName := seed.Branding.DisplayName
	_, err := s.app.Hrms.Services.Hrms.UpsertTenantBranding(ctx, hrmsports.UpsertTenantBrandingCmd{
		TenantID:          tenantID,
		Subdomain:         seed.Subdomain,
		DisplayName:       &displayName,
		Layout:            "vertical",
		ColorMode:         "light",
		SidebarSize:       "default",
		LayoutWidth:       "fluid",
		CardLayout:        "bordered",
		ThemeColor:        seed.Branding.Primary,
		PrimaryColor:      seed.Branding.Primary,
		SecondaryColor:    seed.Branding.Secondary,
		TertiaryColor:     seed.Branding.Tertiary,
		TopbarColor:       "#ffffff",
		SidebarColor:      "#ffffff",
		TopbarBackground:  "#ffffff",
		SidebarBackground: seed.Branding.Sidebar,
		FontFamily:        "Inter, sans-serif",
		Preloader:         false,
	})
	return err
}

func (s *demoSeeder) ensureSubscription(ctx context.Context, tenant identityTenant, tenantName string, planCode string, maxEmployees int32) error {
	plans, err := s.app.Hrms.Services.Hrms.ListSubscriptionPlans(ctx)
	if err != nil {
		return err
	}
	var planID uuid.UUID
	for _, plan := range plans {
		if plan != nil && strings.EqualFold(plan.Code, planCode) {
			planID = plan.ID
			if maxEmployees == 0 {
				maxEmployees = plan.EmployeeLimit
			}
			break
		}
	}
	if planID == uuid.Nil {
		return fmt.Errorf("subscription plan %s not found", planCode)
	}
	if maxEmployees == 0 {
		maxEmployees = 25
	}
	startDate := time.Now().UTC().Format("2006-01-02")
	endDate := time.Now().UTC().AddDate(1, 0, 0).Format("2006-01-02")
	current, err := s.app.Hrms.Services.Hrms.GetCurrentTenantSubscription(ctx, tenant.ID)
	if err == nil && current != nil {
		if _, err := s.app.Hrms.Services.Hrms.UpdateTenantSubscription(ctx, hrmsports.TenantSubscriptionCommand{
			ID:           current.ID,
			TenantID:     tenant.ID,
			PlanID:       &planID,
			StartDate:    startDate,
			EndDate:      endDate,
			Status:       hrmsdomain.SubscriptionStatusActive,
			MaxEmployees: maxEmployees,
		}); err != nil {
			return err
		}
	} else {
		if _, err := s.app.Hrms.Services.Hrms.CreateTenantSubscription(ctx, hrmsports.TenantSubscriptionCommand{
			TenantID:     tenant.ID,
			PlanID:       &planID,
			StartDate:    startDate,
			EndDate:      endDate,
			Status:       hrmsdomain.SubscriptionStatusActive,
			MaxEmployees: maxEmployees,
		}); err != nil {
			return err
		}
	}
	return s.identity.updateTenantSafeFields(ctx, tenant.Raw, tenantName, planCode)
}

type orgRefs struct {
	Branches     map[string]uuid.UUID
	Departments  map[string]uuid.UUID
	Designations map[string]uuid.UUID
}

func (s *demoSeeder) ensureOrgSetup(ctx context.Context, tenantID uuid.UUID, seed demoTenant) (orgRefs, error) {
	refs := orgRefs{Branches: map[string]uuid.UUID{}, Departments: map[string]uuid.UUID{}, Designations: map[string]uuid.UUID{}}
	for _, branch := range seed.Branches {
		item, err := s.ensureBranch(ctx, tenantID, branch)
		if err != nil {
			return refs, err
		}
		refs.Branches[branch.Name] = item.ID
	}
	for _, department := range seed.Departments {
		item, err := s.ensureDepartment(ctx, tenantID, department)
		if err != nil {
			return refs, err
		}
		refs.Departments[department.Name] = item.ID
	}
	for _, designation := range seed.Designations {
		item, err := s.ensureDesignation(ctx, tenantID, designation)
		if err != nil {
			return refs, err
		}
		refs.Designations[designation.Name] = item.ID
	}
	return refs, nil
}

func (s *demoSeeder) ensureBranch(ctx context.Context, tenantID uuid.UUID, seed demoBranch) (*hrmsdomain.Branch, error) {
	items, err := s.app.Hrms.Services.Hrms.ListBranches(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	cmd := hrmsports.BranchCommand{TenantID: tenantID, Name: seed.Name, City: strPtr(seed.City), State: strPtr(seed.State), Country: strPtr(seed.Country), Address: strPtr(seed.Address)}
	for _, item := range items {
		if item != nil && strings.EqualFold(item.Name, seed.Name) {
			cmd.ID = item.ID
			return s.app.Hrms.Services.Hrms.UpdateBranch(ctx, cmd)
		}
	}
	return s.app.Hrms.Services.Hrms.CreateBranch(ctx, cmd)
}

func (s *demoSeeder) ensureDepartment(ctx context.Context, tenantID uuid.UUID, seed demoDepartment) (*hrmsdomain.Department, error) {
	items, err := s.app.Hrms.Services.Hrms.ListDepartments(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	cmd := hrmsports.DepartmentCommand{TenantID: tenantID, Name: seed.Name, ShortCode: seed.ShortCode, Description: strPtr("Demo department seeded by " + demoSource)}
	for _, item := range items {
		if item != nil && strings.EqualFold(item.Name, seed.Name) {
			cmd.ID = item.ID
			return s.app.Hrms.Services.Hrms.UpdateDepartment(ctx, cmd)
		}
	}
	return s.app.Hrms.Services.Hrms.CreateDepartment(ctx, cmd)
}

func (s *demoSeeder) ensureDesignation(ctx context.Context, tenantID uuid.UUID, seed demoDesignation) (*hrmsdomain.Designation, error) {
	items, err := s.app.Hrms.Services.Hrms.ListDesignations(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	cmd := hrmsports.DesignationCommand{TenantID: tenantID, Name: seed.Name, LevelCode: seed.LevelCode, SeniorityRank: seed.SeniorityRank, Description: strPtr("Demo designation seeded by " + demoSource), AttendanceRequired: boolPtr(true)}
	for _, item := range items {
		if item != nil && strings.EqualFold(item.Name, seed.Name) {
			cmd.ID = item.ID
			return s.app.Hrms.Services.Hrms.UpdateDesignation(ctx, cmd)
		}
	}
	return s.app.Hrms.Services.Hrms.CreateDesignation(ctx, cmd)
}

func (s *demoSeeder) ensureEmployeeForUser(ctx context.Context, tenantID uuid.UUID, user identityUser, employeeCode string, person demoPerson, org orgRefs, managerID uuid.UUID) (bool, error) {
	if _, err := s.findEmployeeByCodeOrUser(ctx, tenantID, employeeCode, user.ID); err == nil {
		return false, nil
	}
	departmentID := org.Departments[person.Department]
	branchID := org.Branches[person.Branch]
	designationID := org.Designations[person.Designation]
	joiningDate := time.Now().UTC().AddDate(-1, 0, 0)
	role := person.Role
	if role == "" {
		role = hrmsdomain.RoleEmployee
	}
	employee, err := hrmsdomain.NewEmployee(hrmsdomain.EmployeeInput{
		TenantID:           tenantID,
		UserID:             user.ID,
		EmployeeCode:       &employeeCode,
		Firstname:          person.FirstName,
		Lastname:           &person.LastName,
		Email:              &person.Email,
		Mobile:             &person.Mobile,
		JoiningDate:        &joiningDate,
		DepartmentID:       uuidPtr(departmentID),
		BranchID:           uuidPtr(branchID),
		DesignationID:      uuidPtr(designationID),
		ReportingManagerID: uuidPtr(managerID),
		Role:               &role,
		ExperienceYear:     3,
		ExperienceMonth:    0,
		ProbationStatus:    hrmsdomain.EmployeeProbationConfirmed,
		IsPayrollStaff:     false,
	})
	if err != nil {
		return false, err
	}
	err = s.app.Hrms.Services.Hrms.RunAsSystem(ctx, func(systemCtx context.Context) error {
		_, createErr := s.hrmsStore.CreateEmployee(systemCtx, employee, nil)
		return createErr
	})
	if err != nil {
		return false, err
	}
	return true, nil
}

func (s *demoSeeder) findEmployeeByCode(ctx context.Context, tenantID uuid.UUID, employeeCode string) (*hrmsdomain.Employee, error) {
	return s.findEmployeeByCodeOrUser(ctx, tenantID, employeeCode, uuid.Nil)
}

func (s *demoSeeder) findEmployeeByCodeOrUser(ctx context.Context, tenantID uuid.UUID, employeeCode string, userID uuid.UUID) (*hrmsdomain.Employee, error) {
	items, err := s.app.Hrms.Services.Hrms.ListEmployees(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	for _, item := range items {
		if item != nil && item.EmployeeCode != nil && strings.EqualFold(*item.EmployeeCode, employeeCode) {
			return &item.Employee, nil
		}
		if item != nil && userID != uuid.Nil && item.UserID == userID {
			return &item.Employee, nil
		}
	}
	return nil, fmt.Errorf("employee code %s not found", employeeCode)
}

func (s *demoSeeder) ensureApplicantPortalLink(ctx context.Context, tenantID uuid.UUID, user identityUser, applicant demoPerson) error {
	candidate, err := s.ensureApplicantCandidate(ctx, tenantID, applicant)
	if err != nil {
		return err
	}
	if err := s.ensureApplicantApplication(ctx, tenantID, candidate.ID); err != nil {
		return err
	}
	_, err = s.app.Hrms.Services.Hrms.LinkCandidateApplicantAccount(ctx, hrmsports.CandidateApplicantAccountCommand{
		TenantID:    tenantID,
		CandidateID: candidate.ID,
		UserID:      user.ID,
		Email:       applicant.Email,
		Status:      "active",
		Metadata: map[string]any{
			"source": demoSource,
		},
	})
	return err
}

func (s *demoSeeder) ensureApplicantCandidate(ctx context.Context, tenantID uuid.UUID, applicant demoPerson) (*hrmsdomain.Candidate, error) {
	cmd := hrmsports.CandidateCommand{
		TenantID:          tenantID,
		Firstname:         strPtr(applicant.FirstName),
		Lastname:          strPtr(applicant.LastName),
		Email:             strPtr(applicant.Email),
		Phone:             strPtr(applicant.Mobile),
		CurrentLocation:   strPtr("Bengaluru"),
		PreferredLocation: strPtr("Bengaluru"),
		Source:            strPtr(demoSource),
	}
	candidate, found, err := s.findCandidateByEmail(ctx, tenantID, applicant.Email)
	if err != nil {
		return nil, err
	}
	if found {
		cmd.ID = candidate.ID
		return s.app.Hrms.Services.Hrms.UpdateCandidate(ctx, cmd)
	}
	return s.app.Hrms.Services.Hrms.CreateCandidate(ctx, cmd)
}

func (s *demoSeeder) findCandidateByEmail(ctx context.Context, tenantID uuid.UUID, email string) (*hrmsdomain.Candidate, bool, error) {
	search := strings.ToLower(strings.TrimSpace(email))
	page, err := s.app.Hrms.Services.Hrms.ListCandidates(ctx, hrmsdomain.CandidateFilter{
		TenantID: tenantID,
		Search:   &search,
		Limit:    100,
		Offset:   0,
	})
	if err != nil {
		return nil, false, err
	}
	for _, candidate := range page.Items {
		if candidate != nil && candidate.Email != nil && strings.EqualFold(*candidate.Email, email) {
			return candidate, true, nil
		}
	}
	return nil, false, nil
}

func (s *demoSeeder) ensureApplicantApplication(ctx context.Context, tenantID uuid.UUID, candidateID uuid.UUID) error {
	page, err := s.app.Hrms.Services.Hrms.ListCandidateApplications(ctx, hrmsdomain.CandidateApplicationFilter{
		TenantID:    tenantID,
		CandidateID: &candidateID,
		Limit:       100,
		Offset:      0,
	})
	if err != nil {
		return err
	}
	if len(page.Items) > 0 {
		return nil
	}
	appliedAt := time.Now().UTC().AddDate(0, 0, -7)
	status := hrmsdomain.CandidateApplicationStatusScreening
	_, err = s.app.Hrms.Services.Hrms.CreateCandidateApplication(ctx, hrmsports.CandidateApplicationCommand{
		TenantID:     tenantID,
		CandidateID:  &candidateID,
		Source:       strPtr(demoSource),
		SourceDetail: strPtr("Aanvi careers demo applicant portal seed"),
		Status:       &status,
		Comments:     strPtr("Demo application linked to applicant portal."),
		AppliedAt:    &appliedAt,
	})
	return err
}

type identityFacade struct {
	registration any
	rbac         any
}

func newIdentityFacade(application *app.App) *identityFacade {
	return &identityFacade{
		registration: application.Identity.Services.RegistrationService,
		rbac:         application.Identity.Services.RBACService,
	}
}

type identityTenant struct {
	ID   uuid.UUID
	Name string
	Kind string
	Raw  reflect.Value
}

type identityUser struct {
	ID       uuid.UUID
	TenantID uuid.UUID
	Email    string
	Raw      reflect.Value
}

type registeredTenant struct {
	Tenant identityTenant
	User   identityUser
}

func (f *identityFacade) registerTenant(ctx context.Context, companyName string, admin demoPerson, password string) (registeredTenant, error) {
	result, err := callCommand(f.registration, "RegisterTenant", ctx, func(cmd reflect.Value) {
		setStringField(cmd, "FirstName", admin.FirstName)
		setStringField(cmd, "LastName", admin.LastName)
		setStringField(cmd, "CompanyName", companyName)
		setStringField(cmd, "Email", admin.Email)
		setStringField(cmd, "Mobile", admin.Mobile)
		setStringField(cmd, "Password", password)
		setBoolField(cmd, "AutoVerify", true)
	})
	if err != nil {
		return registeredTenant{}, err
	}
	result = indirect(result)
	return registeredTenant{
		Tenant: tenantFromValue(result.FieldByName("Tenant")),
		User:   userFromValue(result.FieldByName("User")),
	}, nil
}

func (f *identityFacade) createUser(ctx context.Context, tenantID uuid.UUID, person demoPerson, password string, roles []string) (identityUser, error) {
	result, err := callCommand(f.registration, "CreateUser", ctx, func(cmd reflect.Value) {
		setUUIDField(cmd, "TenantID", tenantID)
		setStringField(cmd, "FirstName", person.FirstName)
		setStringField(cmd, "LastName", person.LastName)
		setStringField(cmd, "Email", person.Email)
		setStringField(cmd, "Mobile", person.Mobile)
		setStringField(cmd, "Password", password)
		setStringSliceField(cmd, "Roles", roles)
	})
	if err != nil {
		return identityUser{}, err
	}
	return userFromValue(result), nil
}

func (f *identityFacade) listTenants(ctx context.Context) ([]identityTenant, error) {
	result, err := callValues(f.rbac, "ListTenants", reflect.ValueOf(ctx))
	if err != nil {
		return nil, err
	}
	return tenantsFromSlice(result[0]), nil
}

func (f *identityFacade) findTenantByID(ctx context.Context, tenantID uuid.UUID) (identityTenant, bool, error) {
	tenants, err := f.listTenants(ctx)
	if err != nil {
		return identityTenant{}, false, err
	}
	for _, tenant := range tenants {
		if tenant.ID == tenantID {
			return tenant, true, nil
		}
	}
	return identityTenant{}, false, nil
}

func (f *identityFacade) findTenantByName(ctx context.Context, name string) (identityTenant, bool, error) {
	tenants, err := f.listTenants(ctx)
	if err != nil {
		return identityTenant{}, false, err
	}
	for _, tenant := range tenants {
		if strings.EqualFold(tenant.Name, name) && tenant.Kind == "customer" {
			return tenant, true, nil
		}
	}
	return identityTenant{}, false, nil
}

func (f *identityFacade) findOpsTenant(ctx context.Context) (identityTenant, error) {
	tenants, err := f.listTenants(ctx)
	if err != nil {
		return identityTenant{}, err
	}
	var found identityTenant
	for _, tenant := range tenants {
		if tenant.Kind != "ops" {
			continue
		}
		if found.ID != uuid.Nil {
			return identityTenant{}, errors.New("multiple ops tenants found; refusing platform role seed")
		}
		found = tenant
	}
	if found.ID == uuid.Nil {
		return identityTenant{}, errors.New("ops tenant is missing; refusing platform role seed")
	}
	return found, nil
}

func (f *identityFacade) findUserByEmail(ctx context.Context, email string) (identityUser, bool, error) {
	result, err := callValues(f.registration, "ListUsers", reflect.ValueOf(ctx), reflect.ValueOf(uuid.Nil))
	if err != nil {
		return identityUser{}, false, err
	}
	users := result[0]
	for i := 0; i < users.Len(); i++ {
		user := userFromValue(users.Index(i))
		if strings.EqualFold(user.Email, email) {
			return user, true, nil
		}
	}
	return identityUser{}, false, nil
}

func (f *identityFacade) ensurePlatformRole(ctx context.Context, opsTenantID uuid.UUID, seed platformRoleSeed) (uuid.UUID, bool, bool, error) {
	roles, err := callValues(f.rbac, "ListPlatformRoles", reflect.ValueOf(ctx))
	if err != nil {
		return uuid.Nil, false, false, err
	}
	for i := 0; i < roles[0].Len(); i++ {
		role := indirect(roles[0].Index(i))
		if !strings.EqualFold(stringPtrField(role, "Code"), seed.Code) {
			continue
		}
		roleTenantID := mustUUIDField(role, "TenantID")
		if roleTenantID != opsTenantID {
			return uuid.Nil, false, false, fmt.Errorf("platform role %s resolved outside ops tenant; refusing", seed.Code)
		}
		changed := platformRoleSafeFieldsChanged(role, seed)
		if changed {
			if _, err := callStructArg(f.rbac, "UpdateRole", ctx, func(v reflect.Value) {
				setUUIDField(v, "ID", mustUUIDField(role, "ID"))
				setUUIDField(v, "TenantID", opsTenantID)
				setStringField(v, "Name", seed.Name)
				setStringPtrField(v, "Code", seed.Code)
				setStringPtrField(v, "Description", seed.Description)
			}); err != nil {
				return uuid.Nil, false, false, err
			}
		}
		return mustUUIDField(role, "ID"), false, changed, nil
	}
	created, err := callStructArg(f.rbac, "CreateRole", ctx, func(v reflect.Value) {
		setUUIDField(v, "ID", uuid.New())
		setRoleSeedFields(v, seed, opsTenantID)
		setBoolField(v, "IsSystem", true)
	})
	if err != nil {
		return uuid.Nil, false, false, err
	}
	id, _ := uuidField(created, "ID")
	if id == uuid.Nil {
		return uuid.Nil, false, false, fmt.Errorf("platform role %s was created without id", seed.Code)
	}
	return id, true, false, nil
}

func (f *identityFacade) listPermissionsByFullKey(ctx context.Context) (map[string]uuid.UUID, error) {
	result, err := callValues(f.rbac, "ListPermissions", reflect.ValueOf(ctx))
	if err != nil {
		return nil, err
	}
	permissions := map[string]uuid.UUID{}
	for i := 0; i < result[0].Len(); i++ {
		permission := indirect(result[0].Index(i))
		key := permissionFullKey(permission)
		if key == "" {
			continue
		}
		permissions[key] = mustUUIDField(permission, "ID")
	}
	return permissions, nil
}

func (f *identityFacade) assignPermissionToRole(ctx context.Context, roleID uuid.UUID, permissionID uuid.UUID) error {
	_, err := callValues(f.rbac, "AssignPermissionToRole", reflect.ValueOf(ctx), reflect.ValueOf(roleID), reflect.ValueOf(permissionID))
	return err
}

func (f *identityFacade) ensureHRMSModuleEnabled(ctx context.Context, tenantID uuid.UUID) error {
	return f.ensureModuleEnabled(ctx, tenantID, "hrms")
}

func (f *identityFacade) ensureModuleEnabled(ctx context.Context, tenantID uuid.UUID, code string) error {
	modules, err := callValues(f.rbac, "ListTenantModules", reflect.ValueOf(ctx), reflect.ValueOf(tenantID))
	if err != nil {
		return err
	}
	if hasModuleCode(modules[0], code) {
		return nil
	}
	allModules, err := callValues(f.rbac, "ListModules", reflect.ValueOf(ctx))
	if err != nil {
		return err
	}
	moduleID, ok := moduleIDByCode(allModules[0], code)
	if !ok {
		return fmt.Errorf("%s module is not registered", code)
	}
	_, err = callValues(f.rbac, "EnableModuleForTenant", reflect.ValueOf(ctx), reflect.ValueOf(tenantID), reflect.ValueOf(moduleID))
	return err
}

func (f *identityFacade) ensureRole(ctx context.Context, tenantID uuid.UUID, code string) (uuid.UUID, error) {
	if err := f.ensureHRMSModuleEnabled(ctx, tenantID); err != nil {
		return uuid.Nil, err
	}
	roles, err := callValues(f.rbac, "ListRoles", reflect.ValueOf(ctx), reflect.ValueOf(tenantID))
	if err != nil {
		return uuid.Nil, err
	}
	if roleID, ok := roleIDByCode(roles[0], code); ok {
		return roleID, nil
	}
	templates, err := callValues(f.rbac, "ListTenantRoleTemplates", reflect.ValueOf(ctx), reflect.ValueOf(tenantID))
	if err != nil {
		return uuid.Nil, err
	}
	templateID, ok := templateIDByCode(templates[0], code)
	if !ok {
		return uuid.Nil, fmt.Errorf("role template %s not found", code)
	}
	created, err := callValues(f.rbac, "InstantiateRoleTemplate", reflect.ValueOf(ctx), reflect.ValueOf(tenantID), reflect.ValueOf(templateID))
	if err != nil {
		return uuid.Nil, err
	}
	id, _ := uuidField(created[0], "ID")
	if id == uuid.Nil {
		return uuid.Nil, fmt.Errorf("role %s was created without id", code)
	}
	return id, nil
}

func (f *identityFacade) assignRoleCodes(ctx context.Context, userID uuid.UUID, tenantID uuid.UUID, codes []string) error {
	for _, code := range codes {
		roleID, err := f.ensureRole(ctx, tenantID, code)
		if err != nil {
			return err
		}
		if _, err := callValues(f.registration, "AssignRoleToUser", reflect.ValueOf(ctx), reflect.ValueOf(userID), reflect.ValueOf(tenantID), reflect.ValueOf(roleID)); err != nil {
			return err
		}
	}
	return nil
}

func (f *identityFacade) updateUserPassword(ctx context.Context, userID uuid.UUID, tenantID uuid.UUID, password string) error {
	_, err := callValues(f.registration, "UpdateUserPassword", reflect.ValueOf(ctx), reflect.ValueOf(userID), reflect.ValueOf(tenantID), reflect.ValueOf(password))
	return err
}

func (f *identityFacade) activateUser(ctx context.Context, userID uuid.UUID, tenantID uuid.UUID) error {
	_, err := callValues(f.registration, "ActivateUser", reflect.ValueOf(ctx), reflect.ValueOf(userID), reflect.ValueOf(tenantID))
	return err
}

func (f *identityFacade) updateTenantSafeFields(ctx context.Context, rawTenant reflect.Value, tenantName string, planCode string) error {
	setStringField(indirect(rawTenant), "Name", tenantName)
	setStringPtrField(rawTenant, "SubscriptionPlan", planCode)
	_, err := callValues(f.rbac, "UpdateTenant", reflect.ValueOf(ctx), rawTenant)
	return err
}

func platformRoleSafeFieldsChanged(v reflect.Value, seed platformRoleSeed) bool {
	v = indirect(v)
	return stringField(v, "Name") != seed.Name || stringPtrField(v, "Description") != seed.Description
}

func setRoleSeedFields(v reflect.Value, seed platformRoleSeed, tenantID uuid.UUID) bool {
	v = indirect(v)
	changed := false
	if id, _ := uuidField(v, "TenantID"); id != tenantID {
		setUUIDField(v, "TenantID", tenantID)
		changed = true
	}
	if stringField(v, "Name") != seed.Name {
		setStringField(v, "Name", seed.Name)
		changed = true
	}
	if stringPtrField(v, "Code") != seed.Code {
		setStringPtrField(v, "Code", seed.Code)
		changed = true
	}
	if stringPtrField(v, "Description") != seed.Description {
		setStringPtrField(v, "Description", seed.Description)
		changed = true
	}
	return changed
}

func callCommand(target any, method string, ctx context.Context, fill func(reflect.Value)) (reflect.Value, error) {
	m := reflect.ValueOf(target).MethodByName(method)
	if !m.IsValid() || m.Type().NumIn() < 2 {
		return reflect.Value{}, fmt.Errorf("identity method %s is not available", method)
	}
	cmd := reflect.New(m.Type().In(1)).Elem()
	fill(cmd)
	return callPrepared(m, []reflect.Value{reflect.ValueOf(ctx), cmd})
}

func callStructArg(target any, method string, ctx context.Context, fill func(reflect.Value)) (reflect.Value, error) {
	m := reflect.ValueOf(target).MethodByName(method)
	if !m.IsValid() || m.Type().NumIn() < 2 {
		return reflect.Value{}, fmt.Errorf("identity method %s is not available", method)
	}
	argType := m.Type().In(1)
	if argType.Kind() == reflect.Pointer {
		arg := reflect.New(argType.Elem())
		fill(arg.Elem())
		return callPrepared(m, []reflect.Value{reflect.ValueOf(ctx), arg})
	}
	arg := reflect.New(argType).Elem()
	fill(arg)
	return callPrepared(m, []reflect.Value{reflect.ValueOf(ctx), arg})
}

func callValues(target any, method string, args ...reflect.Value) ([]reflect.Value, error) {
	m := reflect.ValueOf(target).MethodByName(method)
	if !m.IsValid() {
		return nil, fmt.Errorf("identity method %s is not available", method)
	}
	result, err := callPrepared(m, args)
	if err != nil {
		return nil, err
	}
	if !result.IsValid() {
		return nil, nil
	}
	return []reflect.Value{result}, nil
}

func callPrepared(m reflect.Value, args []reflect.Value) (reflect.Value, error) {
	results := m.Call(args)
	if len(results) == 0 {
		return reflect.Value{}, nil
	}
	if last := results[len(results)-1]; isErrorType(last) {
		if !last.IsNil() {
			return reflect.Value{}, last.Interface().(error)
		}
		if len(results) == 1 {
			return reflect.Value{}, nil
		}
		return results[0], nil
	}
	if len(results) == 1 {
		return results[0], nil
	}
	return reflect.Value{}, fmt.Errorf("unexpected result count from %s", m.Type().String())
}

func tenantsFromSlice(slice reflect.Value) []identityTenant {
	out := make([]identityTenant, 0, slice.Len())
	for i := 0; i < slice.Len(); i++ {
		out = append(out, tenantFromValue(slice.Index(i)))
	}
	return out
}

func tenantFromValue(v reflect.Value) identityTenant {
	raw := v
	v = indirect(v)
	return identityTenant{ID: mustUUIDField(v, "ID"), Name: stringField(v, "Name"), Kind: stringField(v, "Kind"), Raw: raw}
}

func userFromValue(v reflect.Value) identityUser {
	raw := v
	v = indirect(v)
	return identityUser{ID: mustUUIDField(v, "ID"), TenantID: mustUUIDField(v, "TenantID"), Email: stringField(v, "Email"), Raw: raw}
}

func hasModuleCode(slice reflect.Value, code string) bool {
	_, ok := moduleIDByCode(slice, code)
	return ok
}

func moduleIDByCode(slice reflect.Value, code string) (uuid.UUID, bool) {
	for i := 0; i < slice.Len(); i++ {
		item := indirect(slice.Index(i))
		if strings.EqualFold(stringField(item, "Code"), code) {
			return mustUUIDField(item, "ID"), true
		}
	}
	return uuid.Nil, false
}

func roleIDByCode(slice reflect.Value, code string) (uuid.UUID, bool) {
	for i := 0; i < slice.Len(); i++ {
		item := indirect(slice.Index(i))
		if strings.EqualFold(stringPtrField(item, "Code"), code) {
			return mustUUIDField(item, "ID"), true
		}
	}
	return uuid.Nil, false
}

func templateIDByCode(slice reflect.Value, code string) (uuid.UUID, bool) {
	for i := 0; i < slice.Len(); i++ {
		item := indirect(slice.Index(i))
		if strings.EqualFold(stringField(item, "Code"), code) {
			return mustUUIDField(item, "ID"), true
		}
	}
	return uuid.Nil, false
}

func setStringField(v reflect.Value, name string, value string) {
	field := v.FieldByName(name)
	if field.IsValid() && field.CanSet() && field.Kind() == reflect.String {
		field.SetString(value)
	}
}

func setBoolField(v reflect.Value, name string, value bool) {
	field := v.FieldByName(name)
	if field.IsValid() && field.CanSet() && field.Kind() == reflect.Bool {
		field.SetBool(value)
	}
}

func setUUIDField(v reflect.Value, name string, value uuid.UUID) {
	field := v.FieldByName(name)
	if field.IsValid() && field.CanSet() && field.Type() == reflect.TypeOf(uuid.UUID{}) {
		field.Set(reflect.ValueOf(value))
	}
}

func setStringSliceField(v reflect.Value, name string, values []string) {
	field := v.FieldByName(name)
	if field.IsValid() && field.CanSet() && field.Kind() == reflect.Slice {
		field.Set(reflect.ValueOf(values))
	}
}

func setStringPtrField(v reflect.Value, name string, value string) {
	v = indirect(v)
	field := v.FieldByName(name)
	if field.IsValid() && field.CanSet() && field.Kind() == reflect.Pointer && field.Type().Elem().Kind() == reflect.String {
		ptr := reflect.New(field.Type().Elem())
		ptr.Elem().SetString(value)
		field.Set(ptr)
	}
}

func uuidField(v reflect.Value, name string) (uuid.UUID, bool) {
	v = indirect(v)
	field := v.FieldByName(name)
	if !field.IsValid() || field.Type() != reflect.TypeOf(uuid.UUID{}) {
		return uuid.Nil, false
	}
	return field.Interface().(uuid.UUID), true
}

func mustUUIDField(v reflect.Value, name string) uuid.UUID {
	id, _ := uuidField(v, name)
	return id
}

func stringField(v reflect.Value, name string) string {
	v = indirect(v)
	field := v.FieldByName(name)
	if !field.IsValid() {
		return ""
	}
	if field.Kind() == reflect.String {
		return field.String()
	}
	if field.Kind() == reflect.Int32 || field.Kind() == reflect.Int {
		return fmt.Sprint(field.Interface())
	}
	return fmt.Sprint(field.Interface())
}

func permissionFullKey(v reflect.Value) string {
	module := strings.TrimSpace(stringField(v, "Module"))
	key := strings.TrimSpace(stringField(v, "Key"))
	if module == "" || key == "" {
		return ""
	}
	if strings.HasPrefix(key, module+".") {
		return key
	}
	return module + "." + key
}

func stringPtrField(v reflect.Value, name string) string {
	v = indirect(v)
	field := v.FieldByName(name)
	if !field.IsValid() || field.Kind() != reflect.Pointer || field.IsNil() {
		return ""
	}
	return field.Elem().String()
}

func indirect(v reflect.Value) reflect.Value {
	for v.IsValid() && (v.Kind() == reflect.Interface || v.Kind() == reflect.Pointer) {
		if v.IsNil() {
			return reflect.Value{}
		}
		v = v.Elem()
	}
	return v
}

func isErrorType(v reflect.Value) bool {
	errorType := reflect.TypeOf((*error)(nil)).Elem()
	return v.IsValid() && v.Type().Implements(errorType)
}

func strPtr(value string) *string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return &value
}

func boolPtr(value bool) *bool {
	return &value
}

func uuidPtr(value uuid.UUID) *uuid.UUID {
	if value == uuid.Nil {
		return nil
	}
	return &value
}

type demoTenant struct {
	Name         string
	Subdomain    string
	PlanCode     string
	MaxEmployees int32
	Branding     demoBranding
	Admin        demoPerson
	Branches     []demoBranch
	Departments  []demoDepartment
	Designations []demoDesignation
	People       []demoPerson
	Applicant    *demoPerson
	Credentials  []string
}

type demoBranding struct {
	DisplayName string
	Primary     string
	Secondary   string
	Tertiary    string
	Sidebar     string
}

type demoBranch struct {
	Name    string
	Address string
	City    string
	State   string
	Country string
}

type demoDepartment struct {
	Name      string
	ShortCode string
}

type demoDesignation struct {
	Name          string
	LevelCode     string
	SeniorityRank int32
}

type demoPerson struct {
	FirstName    string
	LastName     string
	Email        string
	Mobile       string
	EmployeeCode string
	Department   string
	Designation  string
	Branch       string
	Role         string
	ManagerEmail string
}

type platformRoleSeed struct {
	Code                      string
	Name                      string
	Description               string
	SafePermissionKeys        []string
	RecommendedPermissionKeys []string
}

func platformRoles() []platformRoleSeed {
	return []platformRoleSeed{
		{
			Code:               "PLATFORM_ADMIN",
			Name:               "Platform Admin",
			Description:        "Broad platform operations excluding Super Admin-only destructive access.",
			SafePermissionKeys: []string{"identity.admin.access", "identity.tenants.list", "identity.tenants.create", "identity.tenants.update", "identity.users.list", "identity.users.view", "identity.users.create", "identity.users.update", "identity.roles.list", "identity.roles.create", "identity.roles.update"},
			RecommendedPermissionKeys: []string{
				"platform.tenants.read", "platform.tenants.update", "platform.subscriptions.read", "platform.support.read",
			},
		},
		{
			Code:               "PLATFORM_SUPPORT",
			Name:               "Platform Support",
			Description:        "Tenant support and tenant health visibility.",
			SafePermissionKeys: []string{"identity.admin.access", "identity.tenants.list", "identity.users.list", "identity.users.view", "identity.roles.list"},
			RecommendedPermissionKeys: []string{
				"platform.tenants.read", "platform.support.read", "platform.support.manage",
			},
		},
		{
			Code:               "BILLING_OPS",
			Name:               "Billing Ops",
			Description:        "Subscription and billing support.",
			SafePermissionKeys: []string{"identity.admin.access", "identity.tenants.list", "identity.tenants.update"},
			RecommendedPermissionKeys: []string{
				"platform.subscriptions.read", "platform.subscriptions.update", "platform.billing.read",
			},
		},
		{
			Code:               "ACCESS_ADMIN",
			Name:               "Access Admin",
			Description:        "Platform user and role administration.",
			SafePermissionKeys: []string{"identity.admin.access", "identity.users.list", "identity.users.view", "identity.users.create", "identity.users.update", "identity.roles.list", "identity.roles.create", "identity.roles.update"},
			RecommendedPermissionKeys: []string{
				"platform.users.read", "platform.users.create", "platform.users.update", "platform.roles.read", "platform.roles.create", "platform.roles.update", "platform.roles.assign",
			},
		},
		{
			Code:               "READ_ONLY_AUDITOR",
			Name:               "Read-only Auditor",
			Description:        "Read-only audit/platform review.",
			SafePermissionKeys: []string{"identity.admin.access", "identity.tenants.list", "identity.users.list", "identity.users.view", "identity.roles.list"},
			RecommendedPermissionKeys: []string{
				"platform.tenants.read", "platform.subscriptions.read", "platform.audit.read",
			},
		},
		{
			Code:               "IMPLEMENTATION_SPECIALIST",
			Name:               "Implementation Specialist",
			Description:        "Tenant onboarding/setup support.",
			SafePermissionKeys: []string{"identity.admin.access", "identity.tenants.list", "identity.tenants.create", "identity.tenants.update", "identity.users.list", "identity.users.view", "identity.users.create", "identity.users.update"},
			RecommendedPermissionKeys: []string{
				"platform.tenants.read", "platform.tenants.update", "platform.implementation.read", "platform.implementation.update",
			},
		},
		{
			Code:               "PRODUCT_MANAGER",
			Name:               "Product Manager",
			Description:        "Plan/module/product visibility and configuration support.",
			SafePermissionKeys: []string{"identity.admin.access", "identity.tenants.list"},
			RecommendedPermissionKeys: []string{
				"platform.modules.read", "platform.modules.update", "platform.plans.read", "platform.plans.update",
			},
		},
	}
}

func demoTenants() []demoTenant {
	aanviApplicant := demoPerson{FirstName: "Ananya", LastName: "Gupta", Email: "applicant@aanvi.test", Mobile: "9990001011", Role: hrmsdomain.RoleApplicant}
	return []demoTenant{
		{
			Name:         "Aanvi Infotech",
			Subdomain:    "aanvi",
			PlanCode:     "GROWTH_100",
			MaxEmployees: 100,
			Branding:     demoBranding{DisplayName: "Aanvi Infotech", Primary: "#588368", Secondary: "#2f6f7d", Tertiary: "#e87839", Sidebar: "#111827"},
			Admin:        demoPerson{FirstName: "Priya", LastName: "Sharma", Email: "admin@aanvi.test", Mobile: "9990001001", EmployeeCode: "AANVI-ADM-001", Department: "Human Resources", Designation: "HR Manager", Branch: "Bengaluru HQ", Role: hrmsdomain.RoleTenant},
			Branches: []demoBranch{
				{Name: "Bengaluru HQ", Address: "Demo Tower, Indiranagar", City: "Bengaluru", State: "Karnataka", Country: "India"},
				{Name: "Mumbai Sales Office", Address: "Demo Business Centre, BKC", City: "Mumbai", State: "Maharashtra", Country: "India"},
			},
			Departments: []demoDepartment{
				{Name: "Human Resources", ShortCode: "HR"},
				{Name: "Engineering", ShortCode: "ENG"},
				{Name: "Sales", ShortCode: "SAL"},
				{Name: "Finance", ShortCode: "FIN"},
				{Name: "Operations", ShortCode: "OPS"},
			},
			Designations: []demoDesignation{
				{Name: "HR Manager", LevelCode: "M1", SeniorityRank: 500},
				{Name: "Engineering Manager", LevelCode: "M1", SeniorityRank: 500},
				{Name: "Senior Software Engineer", LevelCode: "L4", SeniorityRank: 400},
				{Name: "Software Engineer", LevelCode: "L3", SeniorityRank: 300},
				{Name: "Sales Executive", LevelCode: "L3", SeniorityRank: 300},
				{Name: "Finance Executive", LevelCode: "L3", SeniorityRank: 300},
				{Name: "Operations Coordinator", LevelCode: "L2", SeniorityRank: 200},
			},
			People: []demoPerson{
				{FirstName: "Kavya", LastName: "Menon", Email: "hr@aanvi.test", Mobile: "9990001002", EmployeeCode: "AANVI-HR-001", Department: "Human Resources", Designation: "HR Manager", Branch: "Bengaluru HQ", Role: hrmsdomain.RoleHR, ManagerEmail: "admin@aanvi.test"},
				{FirstName: "Arjun", LastName: "Mehta", Email: "manager@aanvi.test", Mobile: "9990001003", EmployeeCode: "AANVI-MGR-001", Department: "Engineering", Designation: "Engineering Manager", Branch: "Bengaluru HQ", Role: hrmsdomain.RoleManager, ManagerEmail: "admin@aanvi.test"},
				{FirstName: "Nisha", LastName: "Rao", Email: "manager.sales@aanvi.test", Mobile: "9990001004", EmployeeCode: "AANVI-MGR-002", Department: "Sales", Designation: "Sales Executive", Branch: "Mumbai Sales Office", Role: hrmsdomain.RoleManager, ManagerEmail: "admin@aanvi.test"},
				{FirstName: "Rohan", LastName: "Iyer", Email: "employee1@aanvi.test", Mobile: "9990001005", EmployeeCode: "AANVI-EMP-001", Department: "Engineering", Designation: "Senior Software Engineer", Branch: "Bengaluru HQ", Role: hrmsdomain.RoleEmployee, ManagerEmail: "manager@aanvi.test"},
			},
			Applicant: &aanviApplicant,
			Credentials: []string{
				"admin@aanvi.test",
				"hr@aanvi.test",
				"manager@aanvi.test",
				"employee1@aanvi.test",
				"applicant@aanvi.test",
			},
		},
		{
			Name:         "Mash Virtual Pvt Ltd",
			Subdomain:    "mashvirtual",
			PlanCode:     "STARTER",
			MaxEmployees: 25,
			Branding:     demoBranding{DisplayName: "Mash Virtual", Primary: "#2f6f7d", Secondary: "#588368", Tertiary: "#e87839", Sidebar: "#111827"},
			Admin:        demoPerson{FirstName: "Amit", LastName: "Verma", Email: "admin@mashvirtual.test", Mobile: "9990002001", EmployeeCode: "MASH-ADM-001", Department: "People Operations", Designation: "Founder Admin", Branch: "Remote India", Role: hrmsdomain.RoleTenant},
			Branches:     []demoBranch{{Name: "Remote India", Address: "Remote-first India operations", City: "Bengaluru", State: "Karnataka", Country: "India"}},
			Departments: []demoDepartment{
				{Name: "People Operations", ShortCode: "POPS"},
				{Name: "Delivery", ShortCode: "DLV"},
				{Name: "Finance", ShortCode: "FIN"},
			},
			Designations: []demoDesignation{
				{Name: "Founder Admin", LevelCode: "L7", SeniorityRank: 700},
				{Name: "Delivery Manager", LevelCode: "M1", SeniorityRank: 500},
				{Name: "HR Executive", LevelCode: "L3", SeniorityRank: 300},
				{Name: "Associate Consultant", LevelCode: "L2", SeniorityRank: 200},
			},
			People: []demoPerson{
				{FirstName: "Ritu", LastName: "Kapoor", Email: "hr@mashvirtual.test", Mobile: "9990002002", EmployeeCode: "MASH-HR-001", Department: "People Operations", Designation: "HR Executive", Branch: "Remote India", Role: hrmsdomain.RoleHR, ManagerEmail: "admin@mashvirtual.test"},
				{FirstName: "Sagar", LastName: "Jain", Email: "manager@mashvirtual.test", Mobile: "9990002003", EmployeeCode: "MASH-MGR-001", Department: "Delivery", Designation: "Delivery Manager", Branch: "Remote India", Role: hrmsdomain.RoleManager, ManagerEmail: "admin@mashvirtual.test"},
				{FirstName: "Isha", LastName: "Shah", Email: "employee1@mashvirtual.test", Mobile: "9990002004", EmployeeCode: "MASH-EMP-001", Department: "Delivery", Designation: "Associate Consultant", Branch: "Remote India", Role: hrmsdomain.RoleEmployee, ManagerEmail: "manager@mashvirtual.test"},
			},
			Credentials: []string{
				"admin@mashvirtual.test",
				"hr@mashvirtual.test",
				"manager@mashvirtual.test",
				"employee1@mashvirtual.test",
			},
		},
	}
}
