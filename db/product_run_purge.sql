-- 끝난 제품 점검 회차의 DB 기록 삭제(2026-11-23 사용자·팀 결정, 화면 '삭제').
-- 실행 역할은 표 권한이 없고 이 함수 EXECUTE만 가진다. 함수는 설치자(postgres) 소유 SECURITY DEFINER다.
-- E③ 불변 트리거·통제 모드 가드는 이 트랜잭션 안에서만 끄고 커밋 전에 다시 켠다. 트리거 정의·본문은 바꾸지 않는다
-- (시작 역할 검사가 guard_e3_immutable 본문을 고정해 확인함). FK 연쇄·RESTRICT는 그대로라 빠진 자식 행이 있으면
-- DB가 거부하고 전체가 되돌아간다(고아 행 0).
-- 남기는 것: E② 감사 헤드(e2_http_audit_*), audit_checkpoint_*, budget_*, llm_usage, assets. 이 표들은 작업을 FK로
-- 참조하지 않으며, 지우면 다음 회차 시작이 막히거나 예산·감사 근거가 사라진다.
CREATE OR REPLACE FUNCTION abc2lab_purge_task(p_task BIGINT) RETURNS BOOLEAN
LANGUAGE plpgsql SECURITY DEFINER SET search_path = public AS $$
DECLARE
    v_exploration BIGINT;
    v_verdicts BIGINT[];
BEGIN
    IF p_task IS NULL OR p_task <= 0 THEN
        RAISE EXCEPTION 'purge_task_invalid' USING ERRCODE = '22023';
    END IF;
    SELECT exploration_id INTO v_exploration FROM tasks WHERE id = p_task FOR UPDATE;
    IF NOT FOUND THEN
        -- 이미 지운 작업: 다시 불러도 같은 결과(멱등)
        RETURN FALSE;
    END IF;
    ALTER TABLE e3_verdict_links DISABLE TRIGGER trg_e3_link_immutable;
    ALTER TABLE e3_verdict_issuances DISABLE TRIGGER trg_e3_issuance_immutable;
    ALTER TABLE e3_oracle_verdicts DISABLE TRIGGER trg_e3_verdict_immutable;
    ALTER TABLE e3_evidence_manifests DISABLE TRIGGER trg_e3_manifest_immutable;
    ALTER TABLE e3_seal_checks DISABLE TRIGGER trg_e3_seal_check_immutable;
    ALTER TABLE task_control_modes DISABLE TRIGGER trg_task_control_mode;

    DELETE FROM e3_verdict_links WHERE task_id = p_task;
    SELECT array_agg(verdict_id) INTO v_verdicts FROM e3_verdict_issuances WHERE task_id = p_task;
    DELETE FROM e3_verdict_issuances WHERE task_id = p_task;
    IF v_verdicts IS NOT NULL THEN
        DELETE FROM e3_oracle_verdicts WHERE id = ANY(v_verdicts);
    END IF;
    DELETE FROM e3_evidence_manifests WHERE task_id = p_task;
    DELETE FROM e3_seal_checks WHERE task_id = p_task;
    -- 통제 모드 행이 없어야 tasks·task_archives의 controlled 가드가 삭제를 막지 않는다
    DELETE FROM task_control_modes WHERE task_id = p_task;
    IF to_regclass('public.diagnosis_workflows') IS NOT NULL THEN
        EXECUTE 'DELETE FROM diagnosis_workflow_events WHERE job_id IN (SELECT job_id FROM diagnosis_workflows WHERE task_id = $1)' USING p_task;
        EXECUTE 'DELETE FROM diagnosis_workflows WHERE task_id = $1' USING p_task;
    END IF;
    -- findings.task_id 는 SET NULL 이라 작업과 함께 지워지지 않는다. retest·traffic 결속은 연쇄로 지워진다
    DELETE FROM findings WHERE task_id = p_task;
    IF to_regclass('public.llm_records') IS NOT NULL THEN
        EXECUTE 'DELETE FROM llm_records WHERE COALESCE(task_id, '''') = $1' USING p_task::TEXT;
    END IF;
    -- 나머지(탐색 노드·후보·저장 보고서·캡처·요청 승인·e2_prompt_* 등)는 tasks·explorations 연쇄로 지워진다
    DELETE FROM tasks WHERE id = p_task;
    DELETE FROM explorations WHERE id = v_exploration;

    ALTER TABLE e3_verdict_links ENABLE TRIGGER trg_e3_link_immutable;
    ALTER TABLE e3_verdict_issuances ENABLE TRIGGER trg_e3_issuance_immutable;
    ALTER TABLE e3_oracle_verdicts ENABLE TRIGGER trg_e3_verdict_immutable;
    ALTER TABLE e3_evidence_manifests ENABLE TRIGGER trg_e3_manifest_immutable;
    ALTER TABLE e3_seal_checks ENABLE TRIGGER trg_e3_seal_check_immutable;
    ALTER TABLE task_control_modes ENABLE TRIGGER trg_task_control_mode;
    RETURN TRUE;
END;
$$;
-- 새로 만든 함수는 PUBLIC 실행 권한이 기본이다. 기존 역할이 SECURITY DEFINER 함수를 실행할 수 있으면 시작 역할 검사가
-- 거부하므로 같은 트랜잭션에서 바로 거둔다.
REVOKE ALL ON FUNCTION abc2lab_purge_task(BIGINT) FROM PUBLIC;
