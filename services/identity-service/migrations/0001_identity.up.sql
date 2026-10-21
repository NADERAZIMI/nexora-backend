-- B03 identity schema. All timestamps are UTC timestamptz. Sensitive columns
-- store ciphertext, hashes, or public keys only; plaintext passwords and
-- refresh tokens are never persisted.

CREATE SCHEMA IF NOT EXISTS identity;

CREATE TABLE identity.users (
    user_id            uuid PRIMARY KEY,
    email_ciphertext   bytea NOT NULL,
    email_hash         bytea NOT NULL UNIQUE,
    phone_ciphertext   bytea,
    phone_hash         bytea UNIQUE,
    password_hash      text NOT NULL,
    status             text NOT NULL CHECK (status IN ('pending','active','frozen','closed')),
    kyc_level          smallint NOT NULL DEFAULT 0 CHECK (kyc_level BETWEEN 0 AND 5),
    country_code       char(2),
    email_verified_at  timestamptz,
    phone_verified_at  timestamptz,
    mfa_enrolled_at    timestamptz,
    created_at         timestamptz NOT NULL,
    updated_at         timestamptz NOT NULL,
    version            bigint NOT NULL DEFAULT 0
);

CREATE TABLE identity.credentials (
    credential_id       uuid PRIMARY KEY,
    user_id             uuid NOT NULL REFERENCES identity.users(user_id) ON DELETE CASCADE,
    kind                text NOT NULL CHECK (kind IN ('password','totp','webauthn','recovery_code')),
    secret_ciphertext   bytea NOT NULL DEFAULT ''::bytea,
    metadata            jsonb NOT NULL DEFAULT '{}'::jsonb,
    totp_last_step      bigint,
    created_at          timestamptz NOT NULL,
    last_used_at        timestamptz,
    verified_at         timestamptz,
    revoked_at          timestamptz
);
CREATE INDEX credentials_user_kind_idx ON identity.credentials (user_id, kind);

CREATE TABLE identity.sessions (
    session_id          uuid PRIMARY KEY,
    user_id             uuid NOT NULL REFERENCES identity.users(user_id) ON DELETE CASCADE,
    device_fingerprint  text NOT NULL DEFAULT '',
    user_agent          text NOT NULL DEFAULT '',
    ip_hash             bytea NOT NULL,
    mfa                 boolean NOT NULL DEFAULT false,
    created_at          timestamptz NOT NULL,
    last_seen_at        timestamptz NOT NULL,
    expires_at          timestamptz NOT NULL,
    revoked_at          timestamptz
);
CREATE INDEX sessions_user_idx ON identity.sessions (user_id, created_at DESC);
CREATE INDEX sessions_active_idx ON identity.sessions (expires_at) WHERE revoked_at IS NULL;

CREATE TABLE identity.refresh_tokens (
    token_id            uuid PRIMARY KEY,
    session_id          uuid NOT NULL REFERENCES identity.sessions(session_id) ON DELETE CASCADE,
    token_hash          bytea NOT NULL UNIQUE,
    status              text NOT NULL CHECK (status IN ('active','rotated','revoked')),
    expires_at          timestamptz NOT NULL,
    created_at          timestamptz NOT NULL,
    rotated_at          timestamptz
);
CREATE INDEX refresh_tokens_session_idx ON identity.refresh_tokens (session_id, status);

CREATE TABLE identity.api_keys (
    key_id              uuid PRIMARY KEY,
    user_id             uuid NOT NULL REFERENCES identity.users(user_id) ON DELETE CASCADE,
    label               text NOT NULL CHECK (length(label) BETWEEN 1 AND 64),
    key_prefix          text NOT NULL UNIQUE CHECK (length(key_prefix) BETWEEN 8 AND 32),
    algorithm           text NOT NULL CHECK (algorithm IN ('hmac-sha256','ed25519')),
    secret_ciphertext   bytea,
    public_key          bytea,
    scopes              text[] NOT NULL CHECK (cardinality(scopes) > 0 AND scopes <@ ARRAY['read','trade','transfer','withdraw']::text[]),
    ip_allowlist        inet[] NOT NULL DEFAULT '{}'::inet[],
    created_at          timestamptz NOT NULL,
    last_used_at        timestamptz,
    expires_at          timestamptz NOT NULL,
    revoked_at          timestamptz,
    CHECK ((algorithm = 'hmac-sha256' AND secret_ciphertext IS NOT NULL AND octet_length(secret_ciphertext) > 0)
        OR (algorithm = 'ed25519' AND public_key IS NOT NULL AND octet_length(public_key) = 32))
);
CREATE INDEX api_keys_user_idx ON identity.api_keys (user_id, created_at DESC);
CREATE INDEX api_keys_active_idx ON identity.api_keys (expires_at) WHERE revoked_at IS NULL;

CREATE TABLE identity.user_roles (
    user_id             uuid NOT NULL REFERENCES identity.users(user_id) ON DELETE CASCADE,
    role                text NOT NULL CHECK (role IN ('customer','support','risk_operator','compliance','treasury','admin','auditor')),
    granted_by          uuid,
    granted_at          timestamptz NOT NULL,
    PRIMARY KEY (user_id, role)
);

CREATE TABLE identity.verification_tokens (
    token_id            uuid PRIMARY KEY,
    user_id             uuid NOT NULL REFERENCES identity.users(user_id) ON DELETE CASCADE,
    kind                text NOT NULL CHECK (kind IN ('email','phone','password_reset')),
    token_hash          bytea NOT NULL UNIQUE,
    expires_at          timestamptz NOT NULL,
    consumed_at         timestamptz,
    created_at          timestamptz NOT NULL
);
CREATE INDEX verification_user_kind_idx ON identity.verification_tokens (user_id, kind, created_at DESC);

CREATE TABLE identity.mfa_challenges (
    challenge_id        uuid PRIMARY KEY,
    user_id             uuid NOT NULL REFERENCES identity.users(user_id) ON DELETE CASCADE,
    token_hash          bytea NOT NULL UNIQUE,
    methods             text[] NOT NULL,
    expires_at          timestamptz NOT NULL,
    consumed_at         timestamptz,
    created_at          timestamptz NOT NULL
);
CREATE INDEX mfa_challenges_user_idx ON identity.mfa_challenges (user_id, created_at DESC);

CREATE TABLE identity.webauthn_challenges (
    challenge_id        uuid PRIMARY KEY,
    user_id             uuid NOT NULL REFERENCES identity.users(user_id) ON DELETE CASCADE,
    kind                text NOT NULL CHECK (kind IN ('registration','authentication')),
    session_data        jsonb NOT NULL,
    expires_at          timestamptz NOT NULL,
    consumed_at         timestamptz,
    created_at          timestamptz NOT NULL
);
CREATE INDEX webauthn_challenges_user_idx ON identity.webauthn_challenges (user_id, kind, created_at DESC);

CREATE TABLE identity.login_attempts (
    subject_hash        bytea NOT NULL,
    ip_hash             bytea NOT NULL,
    failures            integer NOT NULL CHECK (failures >= 0),
    first_failure_at    timestamptz NOT NULL,
    last_failure_at     timestamptz NOT NULL,
    backoff_until       timestamptz,
    locked_until        timestamptz,
    PRIMARY KEY (subject_hash, ip_hash)
);
CREATE INDEX login_attempts_last_failure_idx ON identity.login_attempts (last_failure_at);

CREATE TABLE identity.audit_events (
    event_id            uuid PRIMARY KEY,
    occurred_at         timestamptz NOT NULL,
    actor_type          text NOT NULL,
    actor_id            text NOT NULL DEFAULT '',
    action              text NOT NULL,
    target_type         text NOT NULL DEFAULT '',
    target_id           text NOT NULL DEFAULT '',
    outcome             text NOT NULL CHECK (outcome IN ('success','denied','failure')),
    ip_hash             bytea,
    user_agent          text NOT NULL DEFAULT '',
    request_id          text NOT NULL DEFAULT '',
    metadata            jsonb NOT NULL DEFAULT '{}'::jsonb,
    prev_hash           bytea,
    event_hash          bytea NOT NULL
);
CREATE INDEX audit_events_occurred_idx ON identity.audit_events (occurred_at DESC);
CREATE INDEX audit_events_actor_idx ON identity.audit_events (actor_id, occurred_at DESC);
CREATE INDEX audit_events_action_idx ON identity.audit_events (action, occurred_at DESC);

CREATE TABLE identity.audit_heads (
    id                  smallint PRIMARY KEY CHECK (id = 1),
    last_hash           bytea,
    last_event_id       uuid,
    updated_at          timestamptz NOT NULL
);
INSERT INTO identity.audit_heads (id, last_hash, last_event_id, updated_at)
VALUES (1, NULL, NULL, now());

CREATE OR REPLACE FUNCTION identity.reject_audit_mutation() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'identity.audit_events is append-only';
END;
$$;
CREATE TRIGGER audit_events_no_update BEFORE UPDATE OR DELETE ON identity.audit_events
FOR EACH ROW EXECUTE FUNCTION identity.reject_audit_mutation();

CREATE TABLE identity.api_key_nonces (
    key_id              uuid NOT NULL REFERENCES identity.api_keys(key_id) ON DELETE CASCADE,
    nonce               text NOT NULL CHECK (length(nonce) BETWEEN 16 AND 128),
    expires_at          timestamptz NOT NULL,
    PRIMARY KEY (key_id, nonce)
);
CREATE INDEX api_key_nonces_expiry_idx ON identity.api_key_nonces (expires_at);
CREATE INDEX api_key_nonces_key_expiry_idx ON identity.api_key_nonces (key_id, expires_at);

CREATE TABLE identity.kyc_cases (
    case_id             uuid PRIMARY KEY,
    user_id             uuid NOT NULL REFERENCES identity.users(user_id) ON DELETE CASCADE,
    provider_ref        text NOT NULL DEFAULT '',
    status              text NOT NULL CHECK (status IN ('submitted','in_review','approved','rejected')),
    level               smallint NOT NULL DEFAULT 0 CHECK (level BETWEEN 0 AND 5),
    reason              text NOT NULL DEFAULT '',
    submitted_at        timestamptz NOT NULL,
    updated_at          timestamptz NOT NULL,
    decided_at          timestamptz,
    decided_by          uuid
);
CREATE INDEX kyc_cases_user_idx ON identity.kyc_cases (user_id, submitted_at DESC);
CREATE INDEX kyc_cases_status_idx ON identity.kyc_cases (status, submitted_at DESC);

CREATE TABLE identity.notification_outbox (
    notification_id     uuid PRIMARY KEY,
    user_id             uuid NOT NULL REFERENCES identity.users(user_id) ON DELETE CASCADE,
    channel             text NOT NULL CHECK (channel IN ('email','phone')),
    kind                text NOT NULL,
    destination_ciphertext bytea NOT NULL,
    payload_ciphertext  bytea NOT NULL,
    status              text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','claimed','delivered','failed')),
    created_at          timestamptz NOT NULL,
    claimed_at          timestamptz,
    delivered_at        timestamptz
);
CREATE INDEX notification_outbox_pending_idx ON identity.notification_outbox (created_at) WHERE status = 'pending';

COMMENT ON SCHEMA identity IS 'NEXORA B03 identity schema';
