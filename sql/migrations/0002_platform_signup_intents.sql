-- Public self-service tenant registration intents.
-- Tenants are provisioned only after the email verification token is consumed.
CREATE SCHEMA IF NOT EXISTS platform;

CREATE TABLE IF NOT EXISTS platform.signup_intents (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    first_name TEXT NOT NULL,
    last_name TEXT NOT NULL,
    email TEXT NOT NULL,
    mobile TEXT NOT NULL,
    password_hash TEXT NOT NULL,
    company_name TEXT NOT NULL,
    subdomain TEXT NOT NULL,
    country CHAR(2) NOT NULL DEFAULT 'IN',
    timezone TEXT NOT NULL DEFAULT 'Asia/Kolkata',
    trial_days INTEGER NOT NULL DEFAULT 30 CHECK (trial_days >= 0),
    status TEXT NOT NULL DEFAULT 'pending_email_verification',
    verification_token_hash TEXT NOT NULL,
    verification_sent_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    email_verified_at TIMESTAMPTZ NULL,
    expires_at TIMESTAMPTZ NOT NULL DEFAULT NOW() + INTERVAL '24 hours',
    provisioned_tenant_id UUID NULL,
    provisioned_user_id UUID NULL,
    provisioned_subscription_id UUID NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT signup_intents_status_check CHECK (
        status IN ('pending_email_verification', 'email_verified', 'provisioned', 'expired', 'cancelled')
    )
);

CREATE INDEX IF NOT EXISTS idx_signup_intents_token_pending
    ON platform.signup_intents (verification_token_hash)
    WHERE status = 'pending_email_verification';

CREATE INDEX IF NOT EXISTS idx_signup_intents_pending_email
    ON platform.signup_intents (lower(email))
    WHERE status = 'pending_email_verification';

CREATE INDEX IF NOT EXISTS idx_signup_intents_pending_mobile
    ON platform.signup_intents (mobile)
    WHERE status = 'pending_email_verification';

CREATE INDEX IF NOT EXISTS idx_signup_intents_pending_subdomain
    ON platform.signup_intents (subdomain)
    WHERE status IN ('pending_email_verification', 'email_verified');
