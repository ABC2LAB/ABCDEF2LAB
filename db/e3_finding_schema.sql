-- First horizontal read issuance. No raw body, credential, or canary value.
CREATE UNIQUE INDEX IF NOT EXISTS uq_e3_tasks_exploration ON tasks(id,exploration_id);
CREATE UNIQUE INDEX IF NOT EXISTS uq_e3_nodes_exploration ON exploration_nodes(id,exploration_id);
CREATE UNIQUE INDEX IF NOT EXISTS uq_e3_findings_task ON findings(id,task_id);

CREATE TABLE IF NOT EXISTS e3_evidence_manifests (
 id BIGSERIAL PRIMARY KEY,
 task_id BIGINT NOT NULL REFERENCES tasks(id) ON DELETE RESTRICT ON UPDATE RESTRICT,
 execution_ref TEXT NOT NULL,
 evidence_revision BIGINT NOT NULL CHECK(evidence_revision>0),
 canonical BYTEA NOT NULL CHECK(octet_length(canonical) BETWEEN 1 AND 65536),
 digest BYTEA NOT NULL CHECK(octet_length(digest)=32),
 frozen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 UNIQUE(task_id,execution_ref,evidence_revision),
 UNIQUE(id,task_id)
);
CREATE TABLE IF NOT EXISTS e3_oracle_verdicts (
 id BIGSERIAL PRIMARY KEY,
 record_version SMALLINT NOT NULL CHECK(record_version=1),
 payload_canonical BYTEA NOT NULL CHECK(octet_length(payload_canonical) BETWEEN 1 AND 65536),
 payload JSONB NOT NULL,
 payload_digest BYTEA NOT NULL CHECK(octet_length(payload_digest)=32),
 oracle_value TEXT NOT NULL CHECK(oracle_value IN ('confirmed','suspected','undecidable','allowed')),
 created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS e3_verdict_issuances (
 id BIGSERIAL PRIMARY KEY,
 verdict_id BIGINT NOT NULL UNIQUE REFERENCES e3_oracle_verdicts(id) ON DELETE RESTRICT ON UPDATE RESTRICT,
 manifest_id BIGINT NOT NULL,
 task_id BIGINT NOT NULL,
 exploration_id BIGINT NOT NULL,
 intent_id BIGINT NOT NULL,
 mode_version BIGINT NOT NULL CHECK(mode_version>0),
 purpose TEXT NOT NULL CHECK(purpose='initial'),
 execution_ref TEXT NOT NULL,
 case_ref TEXT NOT NULL,
 resource_ref TEXT NOT NULL,
 eval_run_id TEXT NULL,
 model_tag TEXT NOT NULL,
 reset_generation TEXT NOT NULL,
 kg_snapshot_ref TEXT NOT NULL,
 kg_policy_version TEXT NOT NULL,
 kg_snapshot_digest BYTEA NOT NULL CHECK(octet_length(kg_snapshot_digest)=32),
 harness_generation_ref TEXT NOT NULL,
 target_instance_ref TEXT NOT NULL,
 operation_key BYTEA NOT NULL UNIQUE CHECK(octet_length(operation_key)=32),
 input_digest BYTEA NOT NULL CHECK(octet_length(input_digest)=32),
 binding_canonical BYTEA NOT NULL CHECK(octet_length(binding_canonical) BETWEEN 1 AND 65536),
 issued_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 FOREIGN KEY(task_id,exploration_id) REFERENCES tasks(id,exploration_id) ON DELETE RESTRICT ON UPDATE RESTRICT,
 FOREIGN KEY(intent_id,exploration_id) REFERENCES exploration_nodes(id,exploration_id) ON DELETE RESTRICT ON UPDATE RESTRICT,
 FOREIGN KEY(manifest_id,task_id) REFERENCES e3_evidence_manifests(id,task_id) ON DELETE RESTRICT ON UPDATE RESTRICT,
 UNIQUE(id,task_id)
);
CREATE TABLE IF NOT EXISTS e3_verdict_links (
 id BIGSERIAL PRIMARY KEY,
 issuance_id BIGINT NOT NULL UNIQUE,
 task_id BIGINT NOT NULL,
 finding_id BIGINT NOT NULL UNIQUE,
 purpose TEXT NOT NULL CHECK(purpose='initial'),
 evidence_version BIGINT NOT NULL CHECK(evidence_version>0),
 linked_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 FOREIGN KEY(issuance_id,task_id) REFERENCES e3_verdict_issuances(id,task_id) ON DELETE RESTRICT ON UPDATE RESTRICT,
 FOREIGN KEY(finding_id,task_id) REFERENCES findings(id,task_id) ON DELETE RESTRICT ON UPDATE RESTRICT
);

CREATE OR REPLACE FUNCTION guard_e3_immutable() RETURNS trigger AS $$
BEGIN
 RAISE EXCEPTION 'e3_immutable_record' USING ERRCODE='23514';
END;
$$ LANGUAGE plpgsql;
DROP TRIGGER IF EXISTS trg_e3_manifest_immutable ON e3_evidence_manifests;
CREATE TRIGGER trg_e3_manifest_immutable BEFORE UPDATE OR DELETE ON e3_evidence_manifests
 FOR EACH ROW EXECUTE FUNCTION guard_e3_immutable();
DROP TRIGGER IF EXISTS trg_e3_manifest_truncate ON e3_evidence_manifests;
CREATE TRIGGER trg_e3_manifest_truncate BEFORE TRUNCATE ON e3_evidence_manifests
 FOR EACH STATEMENT EXECUTE FUNCTION guard_e3_immutable();
DROP TRIGGER IF EXISTS trg_e3_verdict_immutable ON e3_oracle_verdicts;
CREATE TRIGGER trg_e3_verdict_immutable BEFORE UPDATE OR DELETE ON e3_oracle_verdicts
 FOR EACH ROW EXECUTE FUNCTION guard_e3_immutable();
DROP TRIGGER IF EXISTS trg_e3_verdict_truncate ON e3_oracle_verdicts;
CREATE TRIGGER trg_e3_verdict_truncate BEFORE TRUNCATE ON e3_oracle_verdicts
 FOR EACH STATEMENT EXECUTE FUNCTION guard_e3_immutable();
DROP TRIGGER IF EXISTS trg_e3_issuance_immutable ON e3_verdict_issuances;
CREATE TRIGGER trg_e3_issuance_immutable BEFORE UPDATE OR DELETE ON e3_verdict_issuances
 FOR EACH ROW EXECUTE FUNCTION guard_e3_immutable();
DROP TRIGGER IF EXISTS trg_e3_issuance_truncate ON e3_verdict_issuances;
CREATE TRIGGER trg_e3_issuance_truncate BEFORE TRUNCATE ON e3_verdict_issuances
 FOR EACH STATEMENT EXECUTE FUNCTION guard_e3_immutable();
DROP TRIGGER IF EXISTS trg_e3_link_immutable ON e3_verdict_links;
CREATE TRIGGER trg_e3_link_immutable BEFORE UPDATE OR DELETE ON e3_verdict_links
 FOR EACH ROW EXECUTE FUNCTION guard_e3_immutable();
DROP TRIGGER IF EXISTS trg_e3_link_truncate ON e3_verdict_links;
CREATE TRIGGER trg_e3_link_truncate BEFORE TRUNCATE ON e3_verdict_links
 FOR EACH STATEMENT EXECUTE FUNCTION guard_e3_immutable();
