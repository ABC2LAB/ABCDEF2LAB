-- 진단 캡처 v1: 증거·회차 결속과 픽셀 투영을 고정한 추가 전용 자료다.
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
