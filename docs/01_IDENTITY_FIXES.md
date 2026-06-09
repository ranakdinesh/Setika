# IDENTITY MODULE — REQUIRED FIXES

> Apply these fixes in Priority 1 → 2 → 3 order.
> Each fix includes the exact file, the problem, and the replacement code.
> Do not skip any Priority 1 or Priority 2 fix.

---

## PRIORITY 1 — SECURITY FIXES

---

### F1 — OTP Stored as Plaintext

**File:** `modules/identity/core/services/auth_service.go`
**Function:** `RequestOTP()`

**Problem:** OTP is stored directly in `TokenHash` field. If DB is compromised,
all active OTPs are exposed. An attacker with DB read access can authenticate as any user.

**Fix:** Hash the OTP with SHA256 before storing. Send plain OTP to user.
Verify by hashing the input and comparing.

```go
// In RequestOTP() — replace the challenge creation block

import (
    "crypto/sha256"
    "encoding/hex"
)

otp, err := generateOTP(6)
if err != nil {
    return err
}

// Hash before storing — store hash, send plain
otpHash := hashOTP(otp)
challenge := domain.NewChallenge(user.TenantID, user.ID, kind, otpHash, cfg.OTPExpiry)

if err := s.verificationRepo.CreateChallenge(ctx, challenge); err != nil {
    return err
}

return s.commPort.SendOTP(ctx, input.Identifier, input.Channel, otp)
```

```go
// In LoginWithOTP() — replace the comparison block

// Hash the input before comparing
inputHash := hashOTP(input.Code)
if challenge.TokenHash != inputHash {
    return nil, errors.New("invalid OTP")
}
```

```go
// Add this helper function to auth_service.go

func hashOTP(otp string) string {
    h := sha256.Sum256([]byte(otp))
    return hex.EncodeToString(h[:])
}
```

---

### F2 — Secret Key Hardcoded

**File:** `modules/identity/module.go`

**Problem:** `secretKey := "some-secret-key-at-least-32-chars-long"` is a
placeholder that signs all HMAC tokens. Anyone who reads the source can forge tokens.

**Fix:** Read from config. Config reads from `IDENTITY_JWT_SECRET` env var.

```go
// modules/identity/config.go — create this file

package identity

import (
    "time"
    "github.com/ranakdinesh/citual/platform/config"
)

type Config struct {
    JWTSecret     string        `env:"IDENTITY_JWT_SECRET,required"`
    RSAKeyPath    string        `env:"IDENTITY_RSA_KEY_PATH,default=./keys/private.pem"`
    Issuer        string        `env:"IDENTITY_ISSUER,default=http://localhost:8090"`
    AccessTTL     time.Duration `env:"IDENTITY_ACCESS_TTL,default=24h"`
    RefreshTTL    time.Duration `env:"IDENTITY_REFRESH_TTL,default=720h"`
    OTPExpiry     time.Duration `env:"IDENTITY_OTP_EXPIRY,default=2m"`
    OTPMaxAttempts int          `env:"IDENTITY_OTP_MAX_ATTEMPTS,default=3"`
}

func ConfigFrom(global *config.Config) *Config {
    return &global.Identity
}
```

```go
// modules/identity/module.go — use config

func (m *IdentityModule) Mount(infra *platform.Infra) error {
    cfg := ConfigFrom(infra.Config)

    // Use cfg.JWTSecret — never hardcode
    provider, err := oauth2.NewProvider(fositeStore, log, cfg.JWTSecret, cfg.RSAKeyPath, cfg.Issuer)
    if err != nil {
        return fmt.Errorf("identity: oauth2 provider init: %w", err)  // return error, never Fatal
    }
    // ...
}
```

---

### F3 — Token Revocation Stubbed

**File:** `modules/identity/adapters/fosite_store/store.go`

**Problem:** `CreateTokenRevocation`, `GetTokenRevocation`, `DeleteTokenRevocation`
all return `nil`. Revoked tokens continue to be accepted indefinitely.

**Fix:** Use Redis for revocation. Redis TTL matches token expiry automatically.
No cleanup job needed.

```go
// Add Redis to FositeStore

type FositeStore struct {
    store  *postgres.Store
    redis  *redis.Client
    cfg    *identity.Config
}

func NewStore(store *postgres.Store, redis *redis.Client, cfg *identity.Config) *FositeStore {
    return &FositeStore{store: store, redis: redis, cfg: cfg}
}

func revocationKey(sig string) string {
    return fmt.Sprintf("fosite:revoked:%s", sig)
}

func (s *FositeStore) CreateTokenRevocation(ctx context.Context, sig string) error {
    // TTL = access token lifetime so Redis auto-expires stale entries
    return s.redis.Set(ctx, revocationKey(sig), "1", s.cfg.AccessTTL).Err()
}

func (s *FositeStore) GetTokenRevocation(ctx context.Context, sig string) error {
    exists, err := s.redis.Exists(ctx, revocationKey(sig)).Result()
    if err != nil {
        return fosite.ErrServerError.WithWrap(err)
    }
    if exists == 1 {
        return fosite.ErrInactiveToken.WithDebug("token has been revoked")
    }
    return nil
}

func (s *FositeStore) DeleteTokenRevocation(ctx context.Context, sig string) error {
    return s.redis.Del(ctx, revocationKey(sig)).Err()
}
```

---

### F4 — Locked User Can Login

**File:** `modules/identity/core/services/auth_service.go`
**Function:** `Login()`

**Problem:** `IsLocked` field exists on User domain object and is set by
`UpdateUserLockStatus`, but `Login()` only checks `IsActive`, not `IsLocked`.

**Fix:** Add locked check after active check.

```go
func (s *AuthService) Login(ctx context.Context, cmd ports.LoginCmd) (*ports.Session, error) {
    // ... existing user lookup and password verification ...

    if !user.IsActive {
        return nil, errors.New("user is inactive")
    }

    // ADD THIS
    if user.IsLocked {
        s.log.Warn(ctx).Str("user_id", user.ID.String()).Msg("Login denied: account locked")
        return nil, errors.New("account is locked")
    }

    return s.createSession(ctx, user)
}
```

Also add the same check in `LoginWithOTP()` and `LoginWithMagicLink()`:

```go
// LoginWithOTP — add after user lookup
if !user.IsActive || user.IsLocked {
    return nil, errors.New("invalid credentials")
}

// LoginWithMagicLink — add after user lookup
if !user.IsActive || user.IsLocked {
    return nil, errors.New("account unavailable")
}
```

---

### F5 — RSA Private Key File Permissions

**File:** `modules/identity/adapters/oauth2/provider.go`
**Function:** `LoadPrivateKey()`

**Problem:** `os.Create(path)` uses the process umask, typically creating files
as 0644 (world-readable). A private key readable by other OS users is a critical
security failure.

**Fix:** Use `os.OpenFile` with explicit 0600 permissions.

```go
func LoadPrivateKey(path string) (*rsa.PrivateKey, error) {
    b, err := os.ReadFile(path)
    if err == nil {
        block, _ := pem.Decode(b)
        if block != nil {
            return x509.ParsePKCS1PrivateKey(block.Bytes)
        }
    }

    // Generate new key
    key, err := rsa.GenerateKey(rand.Reader, 2048)
    if err != nil {
        return nil, fmt.Errorf("generate RSA key: %w", err)
    }

    // FIX: explicit 0600 permissions — owner read/write only
    keyFile, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
    if err != nil {
        return nil, fmt.Errorf("create key file: %w", err)
    }
    defer keyFile.Close()

    block := &pem.Block{
        Type:  "RSA PRIVATE KEY",
        Bytes: x509.MarshalPKCS1PrivateKey(key),
    }

    if err := pem.Encode(keyFile, block); err != nil {
        return nil, fmt.Errorf("encode key: %w", err)
    }

    return key, nil
}
```

Also: key path must come from config (`cfg.RSAKeyPath`), not hardcoded.
Key directory must exist before first run. Create in bootstrap or document in README.

```bash
# deployments/init.sh — run once before first start
mkdir -p keys
chmod 700 keys
```

---

### F6 — Sessions Not Cleared on Password Reset

**File:** `modules/identity/core/services/auth_service.go`
**Function:** `ResetPassword()`

**Problem:** After a password reset, all existing sessions remain valid.
An attacker who had a session token before the password change can continue
using it indefinitely.

**Fix:** Delete all user sessions after password update.

```go
func (s *AuthService) ResetPassword(ctx context.Context, token, newPassword string) error {
    challenge, err := s.verificationRepo.GetChallengeByToken(ctx, token, domain.VerificationKindPassReset)
    if err != nil || challenge == nil {
        return errors.New("invalid or expired reset token")
    }

    hashedPwd, err := security.HashPassword(newPassword)
    if err != nil {
        return err
    }

    if err := s.userRepo.UpdatePassword(ctx, challenge.UserID, challenge.TenantID, hashedPwd); err != nil {
        return err
    }

    // FIX: invalidate all existing sessions
    if err := s.sessionRepo.DeleteUserSessionsByUserID(ctx, challenge.UserID); err != nil {
        // Log but don't fail — password is already changed
        s.log.Error(ctx).Err(err).Str("user_id", challenge.UserID.String()).
            Msg("Failed to clear sessions after password reset")
    }

    return s.verificationRepo.MarkChallengeConsumed(ctx, challenge.ID)
}
```

---

## PRIORITY 2 — ARCHITECTURE FIXES

---

### A1 — ClientRepo Ports Leak sqlc Types

**File:** `modules/identity/core/ports/ports.go`

**Problem:** `ClientRepo` returns `sqlc.FositeClients` and `FositeService` returns
`*sqlc.FositeClients`. Infrastructure types are leaking through the port layer.

**Fix:** Create `domain.OAuthClient` and use it everywhere in ports.

```go
// modules/identity/core/domain/client.go — replace existing

package domain

import "github.com/google/uuid"

type OAuthClient struct {
    ID            string
    TenantID      uuid.UUID
    Secret        *string       // nil for public clients
    RedirectURIs  []string
    GrantTypes    []string
    ResponseTypes []string
    Scopes        []string
    Audience      []string
    Public        bool
    Active        bool
}
```

```go
// modules/identity/core/ports/ports.go — replace ClientRepo and FositeService

type ClientRepo interface {
    CreateClient(ctx context.Context, client *domain.OAuthClient) (*domain.OAuthClient, error)
    GetClient(ctx context.Context, id string) (*domain.OAuthClient, error)
    GetActiveClient(ctx context.Context, id string) (*domain.OAuthClient, error)
    ListClients(ctx context.Context, tenantID uuid.UUID) ([]*domain.OAuthClient, error)
    ListPublicClients(ctx context.Context) ([]*domain.OAuthClient, error)
    UpdateClient(ctx context.Context, client *domain.OAuthClient) error
    DeleteClient(ctx context.Context, id string) error
}

type FositeService interface {
    CreateClient(ctx context.Context, cmd CreateClientCmd) (*domain.OAuthClient, error)
    GetClient(ctx context.Context, id string) (*domain.OAuthClient, error)
    ListClients(ctx context.Context, tenantID uuid.UUID) ([]*domain.OAuthClient, error)
    UpdateClient(ctx context.Context, id string, cmd UpdateClientCmd) error
    DeleteClient(ctx context.Context, id string) error
    ListPublicClients(ctx context.Context) ([]*domain.OAuthClient, error)
    // OAuth2 Handlers (unchanged)
    NewAuthorizeRequest(ctx context.Context, r *http.Request) (fosite.AuthorizeRequester, error)
    NewAuthorizeResponse(ctx context.Context, ar fosite.AuthorizeRequester, session *SessionUserData) (fosite.AuthorizeResponder, error)
    WriteAuthorizeResponse(ctx context.Context, rw http.ResponseWriter, ar fosite.AuthorizeRequester, resp fosite.AuthorizeResponder)
    WriteAuthorizeError(ctx context.Context, rw http.ResponseWriter, ar fosite.AuthorizeRequester, err error)
    NewAccessRequest(ctx context.Context, r *http.Request) (fosite.AccessRequester, error)
    NewAccessResponse(ctx context.Context, ar fosite.AccessRequester) (fosite.AccessResponder, error)
    WriteAccessResponse(ctx context.Context, rw http.ResponseWriter, ar fosite.AccessRequester, resp fosite.AccessResponder)
    WriteAccessError(ctx context.Context, rw http.ResponseWriter, ar fosite.AccessRequester, err error)
}
```

Update `adapters/postgres/client_repo.go` to map `sqlc.FositeClients` ↔ `domain.OAuthClient`
at the adapter boundary. The mapping stays inside the adapter — no sqlc types cross into core.

---

### A2 — RegistrationService Takes Concrete Store

**File:** `modules/identity/core/services/registration_service.go`

**Problem:** Constructor takes `*postgres.Store` — a concrete infrastructure type.
This makes the service impossible to test without a database.

**Fix:** Accept port interfaces instead.

```go
// Before (wrong)
type RegistrationService struct {
    store *postgres.Store
    // ...
}

func NewRegistrationService(store *postgres.Store, ...) *RegistrationService

// After (correct)
type RegistrationService struct {
    tenantRepo ports.TenantRepo
    userRepo   ports.UserRepo
    roleRepo   ports.RoleRepo
    moduleRepo ports.ModuleRepo
    permRepo   ports.PermissionRepo
    // ...
}

func NewRegistrationService(
    tenantRepo ports.TenantRepo,
    userRepo   ports.UserRepo,
    roleRepo   ports.RoleRepo,
    moduleRepo ports.ModuleRepo,
    permRepo   ports.PermissionRepo,
    temporalClient *temporal.Client,
    log *logger.Loggerx,
) *RegistrationService
```

`*postgres.Store` implements all these interfaces already. Update `module.go` to
pass `store` (which satisfies all interfaces) to the new constructor.

---

### A3 — Module New() Returns Error, Never Fatals

**File:** `modules/identity/module.go`

**Problem:** `log.Fatal` inside the constructor means the whole program exits
without giving the caller a chance to handle the error.

**Fix:** Return `(*IdentityModule, error)`.

```go
// Before
func New(pool *pgxpool.Pool, ...) *Module {
    // ...
    if err != nil {
        log.Fatal(...)  // BAD
    }
}

// After
func New() platform.Module {
    return &IdentityModule{}
}

func (m *IdentityModule) Mount(infra *platform.Infra) error {
    // ...
    provider, err := oauth2.NewProvider(...)
    if err != nil {
        return fmt.Errorf("oauth2 provider: %w", err)  // GOOD
    }
    return nil
}
```

Errors propagate to `platform.Run()` which handles them at the program level.

---

### A4 — Module Exposes Concrete Service Types

**File:** `modules/identity/module.go`

**Problem:** `Module.APIKeyService *services.APIKeyService` and
`Module.RBACService *services.RBACService` expose concrete types.
Callers cannot mock these or swap implementations.

**Fix:** Expose port interfaces only.

```go
// Before
type Module struct {
    Router              chi.Router
    AuthService         ports.AuthService         // already correct
    RegistrationService ports.RegistrationService // already correct
    APIKeyService       *services.APIKeyService   // WRONG
    RBACService         *services.RBACService      // WRONG
    PublicKey           *rsa.PublicKey
}

// After
type Module struct {
    Router              chi.Router
    AuthService         ports.AuthService
    RegistrationService ports.RegistrationService
    APIKeyService       ports.APIKeyService        // define this interface
    RBACService         ports.RBACService          // define this interface
    PublicKey           *rsa.PublicKey
}
```

Add `APIKeyService` and `RBACService` interfaces to `core/ports/ports.go`.

---

### A5 — Remove fmt.Printf from FositeStore

**File:** `modules/identity/adapters/fosite_store/store.go`

**Problem:** Debug print statements in production code paths.

**Fix:** Replace all `fmt.Printf` with structured logger calls.
Pass logger into `FositeStore`.

```go
type FositeStore struct {
    store *postgres.Store
    redis *redis.Client
    cfg   *identity.Config
    log   *logger.Loggerx   // ADD
}

// Replace every fmt.Printf with:
s.log.Debug(ctx).Str("tenant_id", tenantIDStr).Msg("FositeStore: session tenant")
s.log.Error(ctx).Err(err).Msg("FositeStore: CreateSession failed")
```

---

## PRIORITY 3 — CORRECTNESS FIXES

---

### C1 — generateRandomString Error Handling

**File:** `modules/identity/core/services/apikey_service.go`

```go
// Before — silently returns empty string
func generateRandomString(length int) string {
    b := make([]byte, length)
    if _, err := rand.Read(b); err != nil {
        return ""  // SILENT FAILURE
    }
    return hex.EncodeToString(b)[:length]
}

// After — propagate error
func generateRandomString(length int) (string, error) {
    b := make([]byte, length)
    if _, err := rand.Read(b); err != nil {
        return "", fmt.Errorf("generate random string: %w", err)
    }
    return hex.EncodeToString(b)[:length], nil
}

// Update CreateAPIKey to handle error:
prefix, err := generateRandomString(8)
if err != nil {
    return nil, err
}
```

---

### C2 — Email Validation

**File:** `modules/identity/core/domain/user.go`

```go
// Before — too permissive
func looksLikeEmail(s string) bool {
    return strings.Contains(s, "@") && strings.Contains(s, ".")
}

// After — use stdlib
import "net/mail"

func looksLikeEmail(s string) bool {
    _, err := mail.ParseAddress(s)
    return err == nil
}
```

---

### C3 — Bump AuthzVersion on Role Change

**File:** `modules/identity/core/services/registration_service.go`
**Function:** `UpdateUser()`

```go
// After removing and re-assigning roles, bump version:
user.AuthzVersion++
if err := s.store.UpdateUser(ctx, user); err != nil {
    return err
}
```

Also bump in any service that directly modifies role assignments.

---

### C4 — Remove Deprecated Email Field

**File:** `modules/identity/core/ports/ports.go`

```go
// Before
type LoginCmd struct {
    Identifier string
    Email      string // Deprecated: Use Identifier
    Password   string
    IPAddress  string
    UserAgent  string
}

// After
type LoginCmd struct {
    Identifier string  // Email or mobile number
    Password   string
    IPAddress  string
    UserAgent  string
}
```

Update all call sites to use `Identifier`.

---

### C5 — IDTokenIssuer From Config

**File:** `modules/identity/adapters/oauth2/provider.go`

```go
// Update NewProvider signature
func NewProvider(
    storage fosite.Storage,
    logger zerolog.Logger,
    secretKey string,
    rsaKeyPath string,
    issuer string,     // ADD
) (fosite.OAuth2Provider, error) {

    config := &fosite.Config{
        // ...
        IDTokenIssuer: issuer,  // from config, not hardcoded
    }
}
```

---

## RATE LIMITING — ADD TO AUTH ENDPOINTS

Rate limiting is entirely missing. Add before any production deployment.

**Location:** `platform/ratelimit/limiter.go` (platform level, reused by all modules)

```go
package ratelimit

type Limiter struct {
    redis *redis.Client
}

// Sliding window using Redis INCR + EXPIRE

func (l *Limiter) Check(ctx context.Context, key string, limit int, window time.Duration) error {
    count, err := l.redis.Incr(ctx, "rl:"+key).Result()
    if err != nil {
        return nil // fail open — don't block on Redis error
    }
    if count == 1 {
        l.redis.Expire(ctx, "rl:"+key, window)
    }
    if count > int64(limit) {
        return ErrRateLimited
    }
    return nil
}

// Apply in auth handlers:
// OTP request: 3 per identifier per 10 minutes
// Login: 10 per IP per 15 minutes
// Password reset: 3 per email per hour
// Magic link: 3 per identifier per 10 minutes
```

---

## VERIFICATION SUMMARY

After all fixes are applied, verify:

```bash
# Build must pass
go build ./...

# No fmt.Printf in production code paths
grep -r "fmt.Printf" modules/ --include="*.go"
# Should return zero results

# No hardcoded secrets
grep -r "some-secret" modules/ --include="*.go"
grep -r "localhost" modules/ --include="*.go"
# Should return zero results

# All interface implementations verified
# Add compile-time checks:
var _ ports.AuthService         = (*services.AuthService)(nil)
var _ ports.RegistrationService = (*services.RegistrationService)(nil)
var _ ports.FositeService       = (*services.FositeService)(nil)
var _ fosite.Storage            = (*fosite_store.FositeStore)(nil)
```
