CREATE TABLE IF NOT EXISTS evidence_revision_initials (
    revision BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    execution_ref TEXT NOT NULL UNIQUE,
    purpose TEXT NOT NULL DEFAULT 'initial' CHECK (purpose='initial'),
    task_id BIGINT NOT NULL REFERENCES tasks(id),
    exploration_id BIGINT NOT NULL REFERENCES explorations(id),
    intent_id BIGINT NOT NULL REFERENCES exploration_nodes(id),
    mode_version BIGINT NOT NULL CHECK (mode_version>0),
    binding BYTEA NOT NULL CHECK (octet_length(binding)>0 AND octet_length(binding)<=65536),
    input_digest BYTEA NOT NULL CHECK (octet_length(input_digest)=32),
    evidence_digest BYTEA NOT NULL CHECK (octet_length(evidence_digest)=32),
    issued_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);
CREATE TABLE IF NOT EXISTS evidence_revision_operations (
    operation_ref TEXT PRIMARY KEY,
    revision BIGINT NOT NULL REFERENCES evidence_revision_initials(revision),
    request_digest BYTEA NOT NULL CHECK (octet_length(request_digest)=32),
    recorded_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);
CREATE OR REPLACE FUNCTION evidence_revision_immutable() RETURNS trigger
LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'immutable evidence revision'; END $$;
DROP TRIGGER IF EXISTS evidence_revision_initials_immutable ON evidence_revision_initials;
CREATE TRIGGER evidence_revision_initials_immutable BEFORE UPDATE OR DELETE ON evidence_revision_initials
FOR EACH ROW EXECUTE FUNCTION evidence_revision_immutable();
DROP TRIGGER IF EXISTS evidence_revision_operations_immutable ON evidence_revision_operations;
CREATE TRIGGER evidence_revision_operations_immutable BEFORE UPDATE OR DELETE ON evidence_revision_operations
FOR EACH ROW EXECUTE FUNCTION evidence_revision_immutable();
