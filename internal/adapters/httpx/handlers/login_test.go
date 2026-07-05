package handlers

import (
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

func TestEnsureAccessTokenTenantRejectsDifferentTenant(t *testing.T) {
	aanviTenantID := uuid.New()
	mashvirtualTenantID := uuid.New()
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub": "user-1",
		"tid": aanviTenantID.String(),
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	signed, err := token.SignedString([]byte("test-secret"))
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}

	if err := ensureAccessTokenTenant(signed, mashvirtualTenantID); err == nil {
		t.Fatal("expected tenant mismatch error")
	}
}

func TestEnsureAccessTokenTenantAcceptsMatchingTenant(t *testing.T) {
	tenantID := uuid.New()
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub": "user-1",
		"tid": tenantID.String(),
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	signed, err := token.SignedString([]byte("test-secret"))
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}

	if err := ensureAccessTokenTenant(signed, tenantID); err != nil {
		t.Fatalf("expected matching tenant to pass, got %v", err)
	}
}
