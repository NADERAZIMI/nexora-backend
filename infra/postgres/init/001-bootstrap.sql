-- B01 baseline only. Domain schemas are introduced in later phases.
-- Extensions required by the ledger and identifier strategy.
CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE SCHEMA IF NOT EXISTS platform;

COMMENT ON SCHEMA platform IS
    'Cross-cutting platform metadata. Financial schemas are added in B05.';
