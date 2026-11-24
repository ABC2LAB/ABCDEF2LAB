-- 사람이 확인한 이행점검 결과. 원래 조치 판본과 후속 작업의 저장된 근거를 함께 고정한다.
CREATE TABLE IF NOT EXISTS remediation_followup_reviews (
    source_task bigint NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    record_id bigint NOT NULL REFERENCES exploration_nodes(id) ON DELETE CASCADE,
    current_task bigint NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    original_digest text NOT NULL,
    reason text NOT NULL DEFAULT '',
    evidence_entry text NOT NULL,
    step_id text NOT NULL,
    binding_digest text NOT NULL,
    outcome text NOT NULL CHECK (outcome IN ('fixed','reproduced','inconclusive')),
    reviewer text NOT NULL,
    revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
    reviewed_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (source_task, current_task, record_id),
    CHECK (current_task > source_task)
);
