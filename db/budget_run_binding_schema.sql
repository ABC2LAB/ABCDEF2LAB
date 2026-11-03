-- One immutable server-issued RunRef maps to one approval and full execution scope.
-- This is an audit link, not a reservation or permission to send.
CREATE TABLE IF NOT EXISTS budget_run_bindings (
    run_ref TEXT PRIMARY KEY,
    task_ref TEXT NOT NULL,
    case_ref TEXT NOT NULL,
    policy_ref TEXT NOT NULL,
    reset_ref TEXT NOT NULL,
    budget_ref TEXT NOT NULL,
    approval_request_ref TEXT NOT NULL REFERENCES budget_approvals(request_ref),
    issuer_ref TEXT NOT NULL,
    epoch TEXT NOT NULL REFERENCES budget_authority_epochs(epoch),
    expires_at TIMESTAMPTZ NOT NULL,
    bound_at TIMESTAMPTZ NOT NULL,
    CHECK (bound_at < expires_at)
);

-- A run's provenance must match the immutable approval it names.
CREATE UNIQUE INDEX IF NOT EXISTS budget_approval_provenance_uidx ON budget_approvals
    (request_ref, task_ref, budget_ref, policy_ref, reset_ref, issuer_ref, epoch);

-- CREATE TABLE IF NOT EXISTS does not upgrade an earlier table. The catalog
-- guard keeps repeated startup schema initialization idempotent.
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = 'budget_run_bindings'::regclass
          AND conname = 'budget_run_approval_provenance_fk'
    ) THEN
        ALTER TABLE budget_run_bindings ADD CONSTRAINT budget_run_approval_provenance_fk
            FOREIGN KEY (approval_request_ref, task_ref, budget_ref, policy_ref,
                         reset_ref, issuer_ref, epoch)
            REFERENCES budget_approvals
                (request_ref, task_ref, budget_ref, policy_ref, reset_ref, issuer_ref, epoch);
    END IF;
END;
$$;

CREATE OR REPLACE FUNCTION guard_budget_run_binding() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'budget_run_binding_mutation_rejected' USING ERRCODE = '23514';
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_budget_run_binding_record ON budget_run_bindings;
CREATE TRIGGER trg_budget_run_binding_record BEFORE UPDATE OR DELETE ON budget_run_bindings
    FOR EACH ROW EXECUTE FUNCTION guard_budget_run_binding();
DROP TRIGGER IF EXISTS trg_budget_run_binding_truncate ON budget_run_bindings;
CREATE TRIGGER trg_budget_run_binding_truncate BEFORE TRUNCATE ON budget_run_bindings
    FOR EACH STATEMENT EXECUTE FUNCTION guard_budget_run_binding();
