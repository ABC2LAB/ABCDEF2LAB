-- Keep mode authority separate from model-editable task metadata. No backfill:
-- an unclassified task must not silently become an explicit legacy task.
-- No cascading FK: retained findings must not lose their mode fence on deletion.
CREATE TABLE IF NOT EXISTS task_control_modes (
    task_id BIGINT PRIMARY KEY CHECK (task_id > 0),
    mode TEXT NOT NULL CHECK (mode IN ('explicit_legacy', 'controlled')),
    version BIGINT NOT NULL CHECK (version > 0)
);

CREATE OR REPLACE FUNCTION guard_task_control_mode() RETURNS trigger AS $$
BEGIN
    IF TG_OP IN ('DELETE', 'TRUNCATE') THEN
        RAISE EXCEPTION 'task_mode_transition_rejected' USING ERRCODE = '23514';
    END IF;
    IF TG_OP = 'INSERT' THEN
        IF NEW.version <> 1 THEN
            RAISE EXCEPTION 'task_mode_transition_rejected' USING ERRCODE = '23514';
        END IF;
        RETURN NEW;
    END IF;
    IF NEW.task_id = OLD.task_id AND NEW.mode = OLD.mode AND NEW.version = OLD.version THEN
        RETURN NEW;
    END IF;
    IF NEW.task_id = OLD.task_id AND OLD.mode = 'explicit_legacy'
       AND NEW.mode = 'controlled' AND NEW.version = OLD.version + 1 THEN
        RETURN NEW;
    END IF;
    RAISE EXCEPTION 'task_mode_transition_rejected' USING ERRCODE = '23514';
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_task_control_mode ON task_control_modes;
CREATE TRIGGER trg_task_control_mode BEFORE INSERT OR UPDATE OR DELETE ON task_control_modes
    FOR EACH ROW EXECUTE FUNCTION guard_task_control_mode();
DROP TRIGGER IF EXISTS trg_task_control_mode_truncate ON task_control_modes;
CREATE TRIGGER trg_task_control_mode_truncate BEFORE TRUNCATE ON task_control_modes
    FOR EACH STATEMENT EXECUTE FUNCTION guard_task_control_mode();

-- Until verdict provenance participates in deletion/archive snapshots, prevent
-- a controlled finding's task_id from becoming NULL through task removal.
CREATE OR REPLACE FUNCTION guard_controlled_task_removal() RETURNS trigger AS $$
BEGIN
    IF EXISTS(SELECT 1 FROM task_control_modes WHERE task_id=OLD.id AND mode='controlled') THEN
        IF TG_OP = 'DELETE' THEN
            RAISE EXCEPTION 'controlled_task_removal_rejected' USING ERRCODE = '23514';
        END IF;
        IF (NEW.deleted_at IS DISTINCT FROM OLD.deleted_at AND NEW.deleted_at IS NOT NULL)
           OR (NEW.archived_at IS DISTINCT FROM OLD.archived_at AND NEW.archived_at IS NOT NULL) THEN
            RAISE EXCEPTION 'controlled_task_removal_rejected' USING ERRCODE = '23514';
        END IF;
    END IF;
    IF TG_OP = 'DELETE' THEN
        RETURN OLD;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
DROP TRIGGER IF EXISTS trg_controlled_task_removal ON tasks;
CREATE TRIGGER trg_controlled_task_removal BEFORE DELETE OR UPDATE OF deleted_at,archived_at ON tasks
    FOR EACH ROW EXECUTE FUNCTION guard_controlled_task_removal();

CREATE OR REPLACE FUNCTION guard_controlled_task_archive() RETURNS trigger AS $$
BEGIN
    IF TG_OP <> 'INSERT' THEN
        IF EXISTS(SELECT 1 FROM task_control_modes WHERE task_id=OLD.task_id AND mode='controlled') THEN
            RAISE EXCEPTION 'controlled_task_archive_rejected' USING ERRCODE = '23514';
        END IF;
    END IF;
    IF TG_OP <> 'DELETE' THEN
        IF EXISTS(SELECT 1 FROM task_control_modes WHERE task_id=NEW.task_id AND mode='controlled') THEN
            RAISE EXCEPTION 'controlled_task_archive_rejected' USING ERRCODE = '23514';
        END IF;
        RETURN NEW;
    END IF;
    RETURN OLD;
END;
$$ LANGUAGE plpgsql;
DROP TRIGGER IF EXISTS trg_controlled_task_archive ON task_archives;
CREATE TRIGGER trg_controlled_task_archive BEFORE INSERT OR UPDATE OR DELETE ON task_archives
    FOR EACH ROW EXECUTE FUNCTION guard_controlled_task_archive();
