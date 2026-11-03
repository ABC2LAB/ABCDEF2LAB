-- A single live process owns one epoch; a new epoch never restores old ledger usage.
CREATE TABLE IF NOT EXISTS budget_authority_epochs (
    epoch TEXT PRIMARY KEY,
    started_at TIMESTAMPTZ NOT NULL,
    closed_at TIMESTAMPTZ
);

CREATE TABLE IF NOT EXISTS budget_authority_current (
    singleton SMALLINT PRIMARY KEY CHECK (singleton = 1),
    epoch TEXT NOT NULL REFERENCES budget_authority_epochs(epoch),
    closed BOOLEAN NOT NULL DEFAULT FALSE
);

CREATE TABLE IF NOT EXISTS budget_approvals (
    request_ref TEXT PRIMARY KEY,
    issuer_ref TEXT NOT NULL,
    task_ref TEXT NOT NULL,
    budget_ref TEXT NOT NULL UNIQUE,
    policy_ref TEXT NOT NULL,
    reset_ref TEXT NOT NULL,
    max_requests BIGINT NOT NULL CHECK (max_requests > 0),
    valid_from TIMESTAMPTZ NOT NULL,
    valid_until TIMESTAMPTZ NOT NULL,
    epoch TEXT NOT NULL REFERENCES budget_authority_epochs(epoch),
    state TEXT NOT NULL CHECK (state IN ('prepared', 'ready', 'closed')),
    issued_at TIMESTAMPTZ NOT NULL,
    ready_at TIMESTAMPTZ,
    CHECK (valid_from < valid_until),
    CHECK ((state = 'prepared' AND ready_at IS NULL) OR
           (state IN ('ready', 'closed') AND ready_at IS NOT NULL))
);

CREATE TABLE IF NOT EXISTS budget_approval_events (
    id BIGSERIAL PRIMARY KEY,
    request_ref TEXT NOT NULL REFERENCES budget_approvals(request_ref),
    epoch TEXT NOT NULL REFERENCES budget_authority_epochs(epoch),
    state TEXT NOT NULL CHECK (state IN ('prepared', 'ready', 'closed')),
    at TIMESTAMPTZ NOT NULL
);

CREATE OR REPLACE FUNCTION guard_budget_approval_record() RETURNS trigger AS $$
BEGIN
    IF TG_OP IN ('DELETE', 'TRUNCATE') THEN
        RAISE EXCEPTION 'budget_approval_delete_rejected' USING ERRCODE = '23514';
    END IF;
    IF TG_OP = 'UPDATE' AND (
        NEW.request_ref IS DISTINCT FROM OLD.request_ref OR
        NEW.issuer_ref IS DISTINCT FROM OLD.issuer_ref OR
        NEW.task_ref IS DISTINCT FROM OLD.task_ref OR
        NEW.budget_ref IS DISTINCT FROM OLD.budget_ref OR
        NEW.policy_ref IS DISTINCT FROM OLD.policy_ref OR
        NEW.reset_ref IS DISTINCT FROM OLD.reset_ref OR
        NEW.max_requests IS DISTINCT FROM OLD.max_requests OR
        NEW.valid_from IS DISTINCT FROM OLD.valid_from OR
        NEW.valid_until IS DISTINCT FROM OLD.valid_until OR
        NEW.epoch IS DISTINCT FROM OLD.epoch OR
        NEW.issued_at IS DISTINCT FROM OLD.issued_at OR
        (OLD.state = 'prepared' AND NEW.state = 'prepared' AND
            NEW.ready_at IS DISTINCT FROM OLD.ready_at) OR
        (OLD.state <> 'prepared' AND NEW.ready_at IS DISTINCT FROM OLD.ready_at) OR
        (NEW.state <> OLD.state AND NOT
            ((OLD.state = 'prepared' AND NEW.state = 'ready') OR
             (OLD.state = 'ready' AND NEW.state = 'closed')))
    ) THEN
        RAISE EXCEPTION 'budget_approval_mutation_rejected' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_budget_approval_record ON budget_approvals;
CREATE TRIGGER trg_budget_approval_record BEFORE UPDATE OR DELETE ON budget_approvals
    FOR EACH ROW EXECUTE FUNCTION guard_budget_approval_record();
DROP TRIGGER IF EXISTS trg_budget_approval_truncate ON budget_approvals;
CREATE TRIGGER trg_budget_approval_truncate BEFORE TRUNCATE ON budget_approvals
    FOR EACH STATEMENT EXECUTE FUNCTION guard_budget_approval_record();

CREATE OR REPLACE FUNCTION guard_budget_approval_event() RETURNS trigger AS $$
BEGIN
    IF TG_OP <> 'INSERT' THEN
        RAISE EXCEPTION 'budget_approval_event_mutation_rejected' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_budget_approval_event ON budget_approval_events;
CREATE TRIGGER trg_budget_approval_event BEFORE UPDATE OR DELETE ON budget_approval_events
    FOR EACH ROW EXECUTE FUNCTION guard_budget_approval_event();
DROP TRIGGER IF EXISTS trg_budget_approval_event_truncate ON budget_approval_events;
CREATE TRIGGER trg_budget_approval_event_truncate BEFORE TRUNCATE ON budget_approval_events
    FOR EACH STATEMENT EXECUTE FUNCTION guard_budget_approval_event();
