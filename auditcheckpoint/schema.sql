-- Explicit inactive-core migration. Epoch/anchor provisioning is intentionally
-- absent: an empty database is not evidence of a trusted new store generation.
CREATE TABLE IF NOT EXISTS audit_checkpoint_epochs (
    store_ref TEXT PRIMARY KEY,
    epoch_ref TEXT NOT NULL,
    generation BIGINT NOT NULL CHECK (generation > 0),
    anchor_digest BYTEA NOT NULL CHECK (octet_length(anchor_digest) = 32)
);
CREATE TABLE IF NOT EXISTS audit_checkpoint_history (
    store_ref TEXT NOT NULL,
    run_ref TEXT NOT NULL,
    revision BIGINT NOT NULL CHECK (revision > 0),
    epoch_ref TEXT NOT NULL,
    generation BIGINT NOT NULL CHECK (generation > 0),
    anchor_digest BYTEA NOT NULL CHECK (octet_length(anchor_digest) = 32),
    publisher_ref TEXT NOT NULL,
    phase TEXT NOT NULL CHECK (phase IN ('open','sealed','incomplete','quarantined')),
    checkpoint JSONB NOT NULL,
    recorded_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (store_ref, run_ref, revision)
);
CREATE TABLE IF NOT EXISTS audit_checkpoint_heads (
    store_ref TEXT NOT NULL,
    run_ref TEXT NOT NULL,
    epoch_ref TEXT NOT NULL,
    journal_id TEXT NOT NULL,
    revision BIGINT NOT NULL,
    PRIMARY KEY (store_ref, run_ref),
    UNIQUE (store_ref, journal_id),
    FOREIGN KEY (store_ref, run_ref, revision)
      REFERENCES audit_checkpoint_history(store_ref, run_ref, revision)
);
CREATE TABLE IF NOT EXISTS audit_checkpoint_operations (
    store_ref TEXT NOT NULL,
    epoch_ref TEXT NOT NULL,
    operation_ref TEXT NOT NULL,
    request_digest BYTEA NOT NULL CHECK (octet_length(request_digest) = 32),
    receipt_ref TEXT NOT NULL UNIQUE,
    run_ref TEXT NOT NULL,
    revision BIGINT NOT NULL,
    committed_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (store_ref, epoch_ref, operation_ref),
    FOREIGN KEY (store_ref, run_ref, revision)
      REFERENCES audit_checkpoint_history(store_ref, run_ref, revision)
);
CREATE OR REPLACE FUNCTION audit_checkpoint_immutable() RETURNS trigger
LANGUAGE plpgsql AS $$ BEGIN
    RAISE EXCEPTION 'audit checkpoint immutable record';
END $$;
DROP TRIGGER IF EXISTS audit_checkpoint_history_immutable ON audit_checkpoint_history;
CREATE TRIGGER audit_checkpoint_history_immutable BEFORE UPDATE OR DELETE ON audit_checkpoint_history
FOR EACH ROW EXECUTE FUNCTION audit_checkpoint_immutable();
DROP TRIGGER IF EXISTS audit_checkpoint_operations_immutable ON audit_checkpoint_operations;
CREATE TRIGGER audit_checkpoint_operations_immutable BEFORE UPDATE OR DELETE ON audit_checkpoint_operations
FOR EACH ROW EXECUTE FUNCTION audit_checkpoint_immutable();
