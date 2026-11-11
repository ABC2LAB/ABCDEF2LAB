-- 진단 캡처 v1: 증거·회차 결속과 픽셀 투영을 고정한 실패 상태는 성공으로 한 번 승격할 수 있고 성공 캡처는 불변이다.
CREATE TABLE IF NOT EXISTS diagnostic_captures_v1 (
 task_id BIGINT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
 entry_id TEXT NOT NULL CHECK(entry_id ~ '^(candidate|finding|verdict)-[1-9][0-9]*$'),
 step_id TEXT NOT NULL CHECK(step_id ~ '^step-[a-f0-9]{24}$'),
 binding_digest TEXT NOT NULL CHECK(binding_digest ~ '^[a-f0-9]{64}$'),
 evidence_version BIGINT NOT NULL CHECK(evidence_version>0),
 evidence_digest TEXT NOT NULL CHECK(evidence_digest ~ '^[a-f0-9]{64}$'),
 run_ref TEXT NOT NULL CHECK(length(run_ref) BETWEEN 1 AND 256),
 call_ref TEXT NOT NULL CHECK(length(call_ref)<=256),
 capture_ref TEXT NOT NULL,
 state TEXT NOT NULL CHECK(state IN ('captured','not_supported','failed')),
 png BYTEA NOT NULL CHECK(octet_length(png)<=1048576),
 image_digest TEXT NOT NULL,
 width INTEGER NOT NULL,
 height INTEGER NOT NULL,
 captured_at TIMESTAMPTZ,
 mask_policy TEXT NOT NULL,
 created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
 PRIMARY KEY(task_id,entry_id,step_id,binding_digest),
 CHECK ((state='captured' AND capture_ref ~ '^shot-[a-f0-9]{24}$' AND image_digest ~ '^[a-f0-9]{64}$' AND width BETWEEN 1 AND 4096 AND height BETWEEN 1 AND 4096 AND width::BIGINT*height<=8000000 AND octet_length(png)>0 AND captured_at IS NOT NULL AND mask_policy='approved_regions/v1') OR (state<>'captured' AND capture_ref='' AND image_digest='' AND width=0 AND height=0 AND octet_length(png)=0 AND captured_at IS NULL AND mask_policy=''))
);
COMMENT ON TABLE diagnostic_captures_v1 IS 'diagnostic-capture/v1; explicit trusted capture only; reports read without target/model execution';

-- Only a failed/unsupported slot may become a successful capture. Identity is immutable.
CREATE OR REPLACE FUNCTION guard_diagnostic_capture_v1() RETURNS trigger AS $$
BEGIN
 IF TG_OP='TRUNCATE' THEN
  RAISE EXCEPTION 'diagnostic_capture_immutable' USING ERRCODE='23514';
 ELSIF TG_OP='DELETE' THEN
  -- Preserve the existing parent-task cascade; direct replacement is forbidden.
  IF EXISTS(SELECT 1 FROM tasks WHERE id=OLD.task_id) THEN
   RAISE EXCEPTION 'diagnostic_capture_immutable' USING ERRCODE='23514';
  END IF;
  RETURN OLD;
 END IF;
 IF OLD.state='captured' OR NEW.state<>'captured' OR
    ROW(NEW.task_id,NEW.entry_id,NEW.step_id,NEW.binding_digest,NEW.evidence_version,NEW.evidence_digest,NEW.run_ref,NEW.call_ref,NEW.created_at)
    IS DISTINCT FROM ROW(OLD.task_id,OLD.entry_id,OLD.step_id,OLD.binding_digest,OLD.evidence_version,OLD.evidence_digest,OLD.run_ref,OLD.call_ref,OLD.created_at) THEN
  RAISE EXCEPTION 'diagnostic_capture_immutable' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END;
$$ LANGUAGE plpgsql;
DROP TRIGGER IF EXISTS trg_diagnostic_capture_update ON diagnostic_captures_v1;
CREATE TRIGGER trg_diagnostic_capture_update BEFORE UPDATE ON diagnostic_captures_v1
 FOR EACH ROW EXECUTE FUNCTION guard_diagnostic_capture_v1();
DROP TRIGGER IF EXISTS trg_diagnostic_capture_delete ON diagnostic_captures_v1;
CREATE TRIGGER trg_diagnostic_capture_delete BEFORE DELETE ON diagnostic_captures_v1
 FOR EACH ROW EXECUTE FUNCTION guard_diagnostic_capture_v1();
DROP TRIGGER IF EXISTS trg_diagnostic_capture_truncate ON diagnostic_captures_v1;
CREATE TRIGGER trg_diagnostic_capture_truncate BEFORE TRUNCATE ON diagnostic_captures_v1
 FOR EACH STATEMENT EXECUTE FUNCTION guard_diagnostic_capture_v1();
