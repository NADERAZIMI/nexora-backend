-- B04 reference-data configuration schema.
--
-- The authoritative draft and every published version are complete canonical
-- JSON documents stored as bytes. This deliberately avoids a second mutable
-- relational projection that could diverge from the published snapshot.
-- PostgreSQL remains the source of truth; caches are never authoritative.

CREATE SCHEMA IF NOT EXISTS reference;

CREATE SEQUENCE reference.version_seq START WITH 1 INCREMENT BY 1;

CREATE TABLE reference.draft_state (
    id           smallint PRIMARY KEY CHECK (id = 1),
    revision     bigint NOT NULL DEFAULT 0 CHECK (revision >= 0),
    base_version bigint,
    content_hash text CHECK (content_hash IS NULL OR content_hash ~ '^[0-9a-f]{64}$'),
    document     bytea,
    updated_by   uuid,
    updated_at   timestamptz NOT NULL DEFAULT now(),
    CHECK ((document IS NULL) = (content_hash IS NULL))
);

INSERT INTO reference.draft_state (id) VALUES (1);

CREATE TABLE reference.proposals (
    proposal_id       uuid PRIMARY KEY,
    base_version      bigint,
    schema_version    text NOT NULL,
    content_hash      text NOT NULL CHECK (content_hash ~ '^[0-9a-f]{64}$'),
    document          bytea NOT NULL CHECK (octet_length(document) > 0),
    status            text NOT NULL CHECK (status IN ('pending_approval','approved','rejected','published','cancelled')),
    created_by        uuid NOT NULL,
    created_at        timestamptz NOT NULL,
    submitted_at      timestamptz NOT NULL,
    decided_at        timestamptz,
    rejection_reason  text,
    published_version bigint,
    CHECK (status <> 'rejected' OR rejection_reason IS NOT NULL)
);

CREATE INDEX proposals_status_created_idx ON reference.proposals (status, created_at DESC);

CREATE TABLE reference.approvals (
    proposal_id   uuid NOT NULL REFERENCES reference.proposals (proposal_id),
    approver_id   uuid NOT NULL,
    decision      text NOT NULL CHECK (decision IN ('approved','rejected')),
    reason        text,
    proposal_hash text NOT NULL CHECK (proposal_hash ~ '^[0-9a-f]{64}$'),
    decided_at    timestamptz NOT NULL,
    PRIMARY KEY (proposal_id, approver_id),
    CHECK (decision <> 'rejected' OR reason IS NOT NULL)
);

CREATE TABLE reference.versions (
    version          bigint PRIMARY KEY,
    schema_version   text NOT NULL,
    content_hash     text NOT NULL UNIQUE CHECK (content_hash ~ '^[0-9a-f]{64}$'),
    document         bytea NOT NULL CHECK (octet_length(document) > 0),
    effective_at     timestamptz NOT NULL,
    created_by       uuid NOT NULL,
    created_at       timestamptz NOT NULL,
    proposal_id      uuid NOT NULL REFERENCES reference.proposals (proposal_id),
    rollback_of      bigint,
    approval_count   integer NOT NULL CHECK (approval_count >= 2),
    canonicalization text NOT NULL DEFAULT 'sha256/canonical-json-v1'
);

CREATE TABLE reference.activations (
    activation_id   uuid PRIMARY KEY,
    shard_id        text NOT NULL CHECK (shard_id ~ '^[a-z0-9][a-z0-9_.:-]{0,63}$'),
    version         bigint NOT NULL REFERENCES reference.versions (version),
    status          text NOT NULL CHECK (status IN ('pending','active','failed','superseded')),
    expected_version bigint,
    rollback_of     bigint,
    requested_by    uuid NOT NULL,
    requested_at    timestamptz NOT NULL,
    effective_at    timestamptz NOT NULL,
    activated_at    timestamptz,
    failure         text,
    idempotency_key text NOT NULL CHECK (length(idempotency_key) BETWEEN 1 AND 128),
    UNIQUE (shard_id, idempotency_key),
    CHECK (status <> 'failed' OR failure IS NOT NULL),
    CHECK (rollback_of IS NULL OR rollback_of > version)
);

CREATE INDEX activations_pending_idx ON reference.activations (effective_at) WHERE status = 'pending';
CREATE UNIQUE INDEX activations_one_active_idx ON reference.activations (shard_id) WHERE status = 'active';

CREATE TABLE reference.active (
    shard_id      text PRIMARY KEY CHECK (shard_id ~ '^[a-z0-9][a-z0-9_.:-]{0,63}$'),
    version       bigint NOT NULL REFERENCES reference.versions (version),
    activation_id uuid NOT NULL UNIQUE REFERENCES reference.activations (activation_id),
    activated_at  timestamptz NOT NULL
);

CREATE TABLE reference.audit_events (
    event_id    uuid PRIMARY KEY,
    occurred_at timestamptz NOT NULL,
    actor_id    uuid NOT NULL,
    actor_role  text NOT NULL,
    action      text NOT NULL,
    target_type text NOT NULL,
    target_id   text NOT NULL,
    outcome     text NOT NULL,
    request_id  text NOT NULL,
    metadata    jsonb NOT NULL DEFAULT '{}'::jsonb,
    prev_hash   bytea,
    event_hash  bytea NOT NULL
);

CREATE INDEX audit_events_occurred_idx ON reference.audit_events (occurred_at DESC, event_id DESC);

CREATE TABLE reference.audit_heads (
    id            smallint PRIMARY KEY CHECK (id = 1),
    last_hash     bytea,
    last_event_id uuid,
    updated_at    timestamptz NOT NULL DEFAULT now()
);

INSERT INTO reference.audit_heads (id) VALUES (1);

-- Published versions and decisions are immutable.
CREATE FUNCTION reference.reject_mutation() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'reference-data rows are append-only';
END;
$$;

CREATE TRIGGER versions_immutable BEFORE UPDATE OR DELETE ON reference.versions
    FOR EACH ROW EXECUTE FUNCTION reference.reject_mutation();

CREATE TRIGGER approvals_immutable BEFORE UPDATE OR DELETE ON reference.approvals
    FOR EACH ROW EXECUTE FUNCTION reference.reject_mutation();

CREATE TRIGGER audit_events_immutable BEFORE UPDATE OR DELETE ON reference.audit_events
    FOR EACH ROW EXECUTE FUNCTION reference.reject_mutation();

-- A submitted proposal's content is frozen; lifecycle columns may still move.
CREATE FUNCTION reference.protect_proposal_content() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.proposal_id IS DISTINCT FROM OLD.proposal_id
       OR NEW.base_version IS DISTINCT FROM OLD.base_version
       OR NEW.schema_version IS DISTINCT FROM OLD.schema_version
       OR NEW.content_hash IS DISTINCT FROM OLD.content_hash
       OR NEW.document IS DISTINCT FROM OLD.document
       OR NEW.created_by IS DISTINCT FROM OLD.created_by
       OR NEW.created_at IS DISTINCT FROM OLD.created_at
       OR NEW.submitted_at IS DISTINCT FROM OLD.submitted_at THEN
        RAISE EXCEPTION 'submitted proposal content is immutable';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER proposals_freeze BEFORE UPDATE ON reference.proposals
    FOR EACH ROW EXECUTE FUNCTION reference.protect_proposal_content();

CREATE FUNCTION reference.reject_self_approval() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE creator uuid;
BEGIN
    SELECT created_by INTO creator FROM reference.proposals WHERE proposal_id = NEW.proposal_id;
    IF creator IS NULL THEN
        RAISE EXCEPTION 'unknown proposal';
    END IF;
    IF creator = NEW.approver_id AND NEW.decision = 'approved' THEN
        RAISE EXCEPTION 'proposal creator cannot approve their own proposal';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER approvals_no_self BEFORE INSERT ON reference.approvals
    FOR EACH ROW EXECUTE FUNCTION reference.reject_self_approval();

-- Activations move forward through a bounded lifecycle and are never deleted.
CREATE FUNCTION reference.protect_activation() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'activation records are append-only';
    END IF;
    IF NEW.activation_id IS DISTINCT FROM OLD.activation_id
       OR NEW.shard_id IS DISTINCT FROM OLD.shard_id
       OR NEW.version IS DISTINCT FROM OLD.version
       OR NEW.requested_by IS DISTINCT FROM OLD.requested_by
       OR NEW.requested_at IS DISTINCT FROM OLD.requested_at
       OR NEW.idempotency_key IS DISTINCT FROM OLD.idempotency_key THEN
        RAISE EXCEPTION 'activation identity is immutable';
    END IF;
    IF OLD.status = 'active' AND NEW.status NOT IN ('active','superseded') THEN
        RAISE EXCEPTION 'invalid activation transition';
    END IF;
    IF OLD.status IN ('failed','superseded') AND NEW.status <> OLD.status THEN
        RAISE EXCEPTION 'invalid activation transition';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER activations_lifecycle BEFORE UPDATE OR DELETE ON reference.activations
    FOR EACH ROW EXECUTE FUNCTION reference.protect_activation();
