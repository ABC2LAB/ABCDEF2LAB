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
-- 판정 출처 종류(E③ 제품 확정 경로, docs/e3-confirm-path-decision.md §8·§9, 2026-11-18 사용자 결정 ①(가)).
-- 칸을 더하기만 한다. 기존 행은 출처 종류 eval(평가 등록 GET 경로)로 채워지고, 평가 경로의 저장 조건(통제 모드·intent·
-- initial·KG 필수)은 아래 검사로 그대로 남는다. intent·통제 모드 판·KG 칸은 제품(product) 행에서만 비우고, 그 대신 실행
-- 시작 때 동결한 정책 선언 출처가 필수다. 한 행이 두 출처의 칸을 섞어 갖지 못한다. 판정 정본 형식은 출처마다 다르다
-- (평가 record_version 1 = e3record, 제품 record_version 2 = e3-product-verdict/v1).
ALTER TABLE e3_verdict_issuances ADD COLUMN IF NOT EXISTS source_kind TEXT NOT NULL DEFAULT 'eval';
ALTER TABLE e3_verdict_issuances ADD COLUMN IF NOT EXISTS policy_source_ref TEXT NULL;
ALTER TABLE e3_verdict_issuances ADD COLUMN IF NOT EXISTS policy_source_digest BYTEA NULL;
ALTER TABLE e3_verdict_issuances ADD COLUMN IF NOT EXISTS policy_target_approval_revision TEXT NULL;
ALTER TABLE e3_verdict_issuances ADD COLUMN IF NOT EXISTS product_reason_codes JSONB NULL;
ALTER TABLE e3_verdict_issuances ADD COLUMN IF NOT EXISTS candidate_refs JSONB NULL;
ALTER TABLE e3_verdict_issuances ALTER COLUMN intent_id DROP NOT NULL;
ALTER TABLE e3_verdict_issuances ALTER COLUMN mode_version DROP NOT NULL;
ALTER TABLE e3_verdict_issuances ALTER COLUMN kg_snapshot_ref DROP NOT NULL;
ALTER TABLE e3_verdict_issuances ALTER COLUMN kg_policy_version DROP NOT NULL;
ALTER TABLE e3_verdict_issuances ALTER COLUMN kg_snapshot_digest DROP NOT NULL;
-- 시작마다 표 전체를 다시 검사하지 않도록 없을 때만 더한다. CHECK는 NULL이면 통과하므로 제품 칸은 IS NOT NULL을 따로 건다.
DO $e3src$
BEGIN
 IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='e3_issuance_source_fields' AND conrelid='e3_verdict_issuances'::regclass) THEN
  ALTER TABLE e3_verdict_issuances ADD CONSTRAINT e3_issuance_source_fields CHECK (
   (source_kind='eval' AND intent_id IS NOT NULL AND mode_version IS NOT NULL AND kg_snapshot_ref IS NOT NULL
    AND kg_policy_version IS NOT NULL AND kg_snapshot_digest IS NOT NULL
    AND policy_source_ref IS NULL AND policy_source_digest IS NULL AND policy_target_approval_revision IS NULL
    AND product_reason_codes IS NULL AND candidate_refs IS NULL)
   OR
   (source_kind='product' AND intent_id IS NULL AND mode_version IS NULL AND kg_snapshot_ref IS NULL
    AND kg_policy_version IS NULL AND kg_snapshot_digest IS NULL AND eval_run_id IS NULL
    AND policy_source_ref IS NOT NULL AND policy_source_digest IS NOT NULL AND octet_length(policy_source_digest)=32
    AND policy_target_approval_revision IS NOT NULL
    AND product_reason_codes IS NOT NULL AND jsonb_typeof(product_reason_codes)='array'
    AND candidate_refs IS NOT NULL AND jsonb_typeof(candidate_refs)='array'));
 END IF;
 IF EXISTS (SELECT 1 FROM pg_constraint WHERE conname='e3_oracle_verdicts_record_version_check' AND conrelid='e3_oracle_verdicts'::regclass) THEN
  ALTER TABLE e3_oracle_verdicts DROP CONSTRAINT e3_oracle_verdicts_record_version_check;
 END IF;
 IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='e3_verdict_record_version' AND conrelid='e3_oracle_verdicts'::regclass) THEN
  ALTER TABLE e3_oracle_verdicts ADD CONSTRAINT e3_verdict_record_version CHECK (record_version IN (1,2));
 END IF;
END
$e3src$;

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

-- E③ 제품 판정 봉인 확인(E1, docs/contracts/e3-product-verdict-store.md §6). E② Host가 봉인 뒤 근거 체크포인트마다 봉인
-- head 포함 여부를 확인한 결과를 회차마다 한 번만 남긴다. 판정 행과 따로 두고 바꾸지 않는다. 봉인 실패·ACK 불명·interrupted는
-- 행이 없다(표시는 "봉인 미확인").
CREATE TABLE IF NOT EXISTS e3_seal_checks (
 id BIGSERIAL PRIMARY KEY,
 task_id BIGINT NOT NULL REFERENCES tasks(id) ON DELETE RESTRICT ON UPDATE RESTRICT,
 run_ref TEXT NOT NULL CHECK (char_length(run_ref) BETWEEN 1 AND 128),
 journal_id TEXT NOT NULL CHECK (char_length(journal_id) BETWEEN 1 AND 128),
 head_sequence BIGINT NOT NULL CHECK (head_sequence >= 0),
 head_mac TEXT NOT NULL CHECK (head_mac ~ '^[0-9a-f]{64}$'),
 checks JSONB NOT NULL CHECK (jsonb_typeof(checks) = 'array'),
 recorded_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 UNIQUE(task_id, run_ref)
);
DROP TRIGGER IF EXISTS trg_e3_seal_check_immutable ON e3_seal_checks;
CREATE TRIGGER trg_e3_seal_check_immutable BEFORE UPDATE OR DELETE ON e3_seal_checks
 FOR EACH ROW EXECUTE FUNCTION guard_e3_immutable();
DROP TRIGGER IF EXISTS trg_e3_seal_check_truncate ON e3_seal_checks;
CREATE TRIGGER trg_e3_seal_check_truncate BEFORE TRUNCATE ON e3_seal_checks
 FOR EACH STATEMENT EXECUTE FUNCTION guard_e3_immutable();
