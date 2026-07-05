package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	hrmsdomain "github.com/ranakdinesh/setika-hrms/core/domain"
	hrmsports "github.com/ranakdinesh/setika-hrms/core/ports"
	identityadapter "github.com/ranakdinesh/setika/internal/adapters/identity"
)

type applicantApplyRequest struct {
	JobPostingID      uuid.UUID `json:"job_posting_id"`
	Firstname         string    `json:"firstname"`
	Lastname          string    `json:"lastname"`
	Email             string    `json:"email"`
	Phone             string    `json:"phone"`
	Password          string    `json:"password"`
	DOB               string    `json:"dob"`
	ResumeURL         string    `json:"resume_url"`
	CoverLetter       string    `json:"cover_letter"`
	TotalExperience   *float64  `json:"total_experience,omitempty"`
	CurrentCompany    string    `json:"current_company"`
	ExpectedCTC       *float64  `json:"expected_ctc,omitempty"`
	NoticePeriod      *int32    `json:"notice_period,omitempty"`
	PreferredLocation string    `json:"preferred_location"`
	Consent           bool      `json:"consent"`
}

type applicantApplyResponse struct {
	UserID      uuid.UUID                             `json:"user_id"`
	Account     *hrmsdomain.CandidateApplicantAccount `json:"account"`
	Candidate   *hrmsdomain.Candidate                 `json:"candidate"`
	Application *hrmsdomain.CandidateApplication      `json:"application"`
	Message     string                                `json:"message"`
}

func (h *Handler) ApplyForJob(w http.ResponseWriter, r *http.Request) {
	var req applicantApplyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.logSignupError(r.Context(), "decode applicant apply request", err)
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	tenantID, tenantScoped, err := h.resolveTenantIDFromLoginHost(r)
	if err != nil {
		h.logSignupError(r.Context(), "resolve applicant tenant host", err)
		writeError(w, http.StatusNotFound, "tenant careers domain not found")
		return
	}
	if !tenantScoped || tenantID == uuid.Nil {
		writeError(w, http.StatusBadRequest, "apply from the tenant careers domain")
		return
	}
	if err := req.validate(); err != nil {
		h.logSignupError(r.Context(), "validate applicant apply request", err)
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	userID, err := h.ensureApplicantIdentity(r, tenantID, req)
	if err != nil {
		h.logSignupError(r.Context(), "ensure applicant identity", err)
		writeError(w, http.StatusUnauthorized, err.Error())
		return
	}

	var response applicantApplyResponse
	if err := h.hrms.Services.Hrms.RunAsSystem(r.Context(), func(systemCtx context.Context) error {
		posting, err := h.hrms.Services.Hrms.GetJobPosting(systemCtx, tenantID, req.JobPostingID)
		if err != nil {
			return err
		}
		if !posting.IsPublished || posting.JobStatus == nil || *posting.JobStatus != hrmsdomain.JobPostingStatusOpen {
			return errors.New("job opening is not accepting applications")
		}

		candidate, err := h.resolveApplicantCandidate(systemCtx, tenantID, userID, req)
		if err != nil {
			return err
		}
		source := "Career Portal"
		appliedAt := time.Now().UTC()
		status := hrmsdomain.CandidateApplicationStatusNew
		application, err := h.hrms.Services.Hrms.CreateCandidateApplication(systemCtx, hrmsports.CandidateApplicationCommand{
			TenantID:     tenantID,
			CandidateID:  &candidate.ID,
			JobPostingID: &req.JobPostingID,
			ResumeURL:    applicantStringPtr(req.ResumeURL),
			CoverLetter:  applicantStringPtr(req.CoverLetter),
			ExpectedCTC:  req.ExpectedCTC,
			NoticePeriod: req.NoticePeriod,
			Source:       &source,
			SourceDetail: applicantStringPtr(requestHost(r)),
			Status:       &status,
			AppliedAt:    &appliedAt,
		})
		if err != nil {
			return err
		}
		consentAt := time.Now().UTC()
		remoteIP := requestRemoteIP(r)
		account, err := h.hrms.Services.Hrms.LinkCandidateApplicantAccount(systemCtx, hrmsports.CandidateApplicantAccountCommand{
			TenantID:    tenantID,
			CandidateID: candidate.ID,
			UserID:      userID,
			Email:       req.Email,
			ConsentAt:   &consentAt,
			ConsentIP:   applicantStringPtr(remoteIP),
			Metadata: map[string]any{
				"job_posting_id": req.JobPostingID.String(),
				"source_host":    requestHost(r),
			},
		})
		if err != nil {
			return err
		}
		response = applicantApplyResponse{
			UserID:      userID,
			Account:     account,
			Candidate:   candidate,
			Application: application,
			Message:     "Application submitted. Sign in with this email to track the status.",
		}
		return nil
	}); err != nil {
		h.logSignupError(r.Context(), "create applicant application", err)
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(response)
}

func (req *applicantApplyRequest) validate() error {
	req.Firstname = strings.TrimSpace(req.Firstname)
	req.Lastname = strings.TrimSpace(req.Lastname)
	req.Email = strings.TrimSpace(strings.ToLower(req.Email))
	req.Phone = strings.TrimSpace(req.Phone)
	req.Password = strings.TrimSpace(req.Password)
	if req.JobPostingID == uuid.Nil {
		return errors.New("job posting is required")
	}
	if req.Firstname == "" || req.Lastname == "" || req.Email == "" || req.Password == "" {
		return errors.New("first name, last name, email, and password are required")
	}
	if !req.Consent {
		return errors.New("candidate account consent is required")
	}
	return nil
}

func (h *Handler) ensureApplicantIdentity(r *http.Request, tenantID uuid.UUID, req applicantApplyRequest) (uuid.UUID, error) {
	adapter, err := identityadapter.NewEmployeeIdentityAdapter(h.identity)
	if err != nil {
		return uuid.Nil, err
	}
	identity, err := adapter.CreateEmployeeIdentity(r.Context(), hrmsports.CreateEmployeeIdentityCommand{
		TenantID:  tenantID,
		FirstName: req.Firstname,
		LastName:  req.Lastname,
		Email:     req.Email,
		Mobile:    req.Phone,
		Password:  req.Password,
		Role:      hrmsdomain.RoleApplicant,
	})
	if err == nil {
		return identity.UserID, nil
	}
	if !isApplicantExistingIdentityError(err) {
		return uuid.Nil, err
	}
	login, loginErr := h.loginThroughIdentity(r, loginRequest{Identifier: req.Email, Password: req.Password})
	if loginErr != nil {
		return uuid.Nil, errors.New("email is already registered; use the existing password to apply")
	}
	if err := ensureAccessTokenTenant(login.AccessToken, tenantID); err != nil {
		return uuid.Nil, errors.New("email is already registered for another tenant")
	}
	userID, err := accessTokenUserID(login.AccessToken)
	if err != nil {
		return uuid.Nil, err
	}
	if err := adapter.AssignEmployeeRole(r.Context(), hrmsports.AssignEmployeeRoleCommand{TenantID: tenantID, UserID: userID, Role: hrmsdomain.RoleApplicant}); err != nil {
		return uuid.Nil, err
	}
	return userID, nil
}

func (h *Handler) resolveApplicantCandidate(ctx context.Context, tenantID uuid.UUID, userID uuid.UUID, req applicantApplyRequest) (*hrmsdomain.Candidate, error) {
	portal, err := h.hrms.Services.Hrms.GetApplicantPortal(ctx, tenantID, userID)
	if err == nil && portal != nil && portal.Candidate != nil {
		return portal.Candidate, nil
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) && !strings.Contains(strings.ToLower(err.Error()), "no rows") {
		return nil, err
	}
	source := "Career Portal"
	dob, err := applicantDate(req.DOB)
	if err != nil {
		return nil, err
	}
	return h.hrms.Services.Hrms.CreateCandidate(ctx, hrmsports.CandidateCommand{
		TenantID:          tenantID,
		Firstname:         applicantStringPtr(req.Firstname),
		Lastname:          applicantStringPtr(req.Lastname),
		Email:             applicantStringPtr(req.Email),
		Phone:             applicantStringPtr(req.Phone),
		DOB:               dob,
		TotalExperience:   req.TotalExperience,
		CurrentCompany:    applicantStringPtr(req.CurrentCompany),
		ExpectedSalary:    req.ExpectedCTC,
		NoticePeriod:      req.NoticePeriod,
		PreferredLocation: applicantStringPtr(req.PreferredLocation),
		Source:            &source,
		ResumeURL:         applicantStringPtr(req.ResumeURL),
	})
}

func accessTokenUserID(accessToken string) (uuid.UUID, error) {
	claims := jwt.MapClaims{}
	_, _, err := jwt.NewParser().ParseUnverified(accessToken, claims)
	if err != nil {
		return uuid.Nil, err
	}
	raw := firstNonEmptyString(claimsString(claims, "sub"), claimsString(claims, "uid"), claimsString(claims, "user_id"))
	if raw == "" {
		return uuid.Nil, errors.New("identity token is missing user id")
	}
	return uuid.Parse(raw)
}

func isApplicantExistingIdentityError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "email already registered") || strings.Contains(msg, "mobile already registered") || strings.Contains(msg, "already exists")
}

func applicantDate(value string) (*time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	parsed, err := time.Parse("2006-01-02", value)
	if err != nil {
		return nil, errors.New("invalid date of birth")
	}
	return &parsed, nil
}

func requestRemoteIP(r *http.Request) string {
	if r == nil {
		return ""
	}
	if forwarded := strings.TrimSpace(r.Header.Get("X-Forwarded-For")); forwarded != "" {
		return strings.TrimSpace(strings.Split(forwarded, ",")[0])
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return strings.TrimSpace(r.RemoteAddr)
}

func applicantStringPtr(value string) *string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return &value
}
