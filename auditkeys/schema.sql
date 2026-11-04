CREATE TABLE IF NOT EXISTS audit_key_registry_keys (
 namespace TEXT NOT NULL,
 key_ref TEXT NOT NULL,
 secret_version_ref TEXT NOT NULL,
 secret_identity TEXT NOT NULL,
 predecessor_ref TEXT,
 state TEXT NOT NULL CHECK(state IN ('prepared','active','retiring','retired','compromised','destroyed')),
 revision BIGINT NOT NULL CHECK(revision>0),
 updated_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(namespace,key_ref),
 UNIQUE(namespace,secret_version_ref),
 UNIQUE(namespace,secret_identity),
 FOREIGN KEY(namespace,predecessor_ref) REFERENCES audit_key_registry_keys(namespace,key_ref) ON UPDATE RESTRICT ON DELETE RESTRICT,
 CHECK(predecessor_ref IS NULL OR predecessor_ref<>key_ref)
);
CREATE TABLE IF NOT EXISTS audit_key_registry_history (
 namespace TEXT NOT NULL,
 key_ref TEXT NOT NULL,
 revision BIGINT NOT NULL CHECK(revision>0),
 from_state TEXT NOT NULL,
 state TEXT NOT NULL,
 operation_ref TEXT NOT NULL,
 actor_ref TEXT NOT NULL,
 policy_ref TEXT NOT NULL,
 policy_revision BIGINT NOT NULL CHECK(policy_revision>0),
 mapping_ref TEXT NOT NULL,
 authorization_ref TEXT NOT NULL,
 activation_ref TEXT NOT NULL,
 drain_ref TEXT NOT NULL,
 dependency_ref TEXT NOT NULL,
 copy_lifetime_ref TEXT NOT NULL,
 disposal_ref TEXT NOT NULL,
 unused_ref TEXT NOT NULL,
 changed_at TIMESTAMPTZ NOT NULL,
 PRIMARY KEY(namespace,key_ref,revision),
 UNIQUE(namespace,operation_ref),
 FOREIGN KEY(namespace,key_ref) REFERENCES audit_key_registry_keys(namespace,key_ref) ON UPDATE RESTRICT ON DELETE RESTRICT
);
CREATE TABLE IF NOT EXISTS audit_key_registry_operations (
 namespace TEXT NOT NULL,
 operation_ref TEXT NOT NULL,
 request_digest BYTEA NOT NULL CHECK(octet_length(request_digest)=32),
 key_ref TEXT NOT NULL,
 revision BIGINT NOT NULL,
 state TEXT NOT NULL,
 PRIMARY KEY(namespace,operation_ref),
 FOREIGN KEY(namespace,key_ref,revision) REFERENCES audit_key_registry_history(namespace,key_ref,revision) ON UPDATE RESTRICT ON DELETE RESTRICT
);
CREATE OR REPLACE FUNCTION audit_key_registry_guard_key() RETURNS trigger AS $$
BEGIN
 IF TG_OP='DELETE' OR TG_OP='TRUNCATE' THEN
  RAISE EXCEPTION 'audit_key_registry_immutable_mapping' USING ERRCODE='23514';
 END IF;
 IF TG_OP='INSERT' THEN
  IF NEW.state<>'prepared' OR NEW.revision<>1 THEN
   RAISE EXCEPTION 'audit_key_registry_initial_state' USING ERRCODE='23514';
  END IF;
  RETURN NEW;
 END IF;
 IF (NEW.namespace,NEW.key_ref,NEW.secret_version_ref,NEW.secret_identity,NEW.predecessor_ref)
  IS DISTINCT FROM (OLD.namespace,OLD.key_ref,OLD.secret_version_ref,OLD.secret_identity,OLD.predecessor_ref) THEN
  RAISE EXCEPTION 'audit_key_registry_immutable_mapping' USING ERRCODE='23514';
 END IF;
 IF NEW.revision<>OLD.revision+1 OR NOT (
  (OLD.state='prepared' AND NEW.state IN ('active','compromised','destroyed')) OR
  (OLD.state='active' AND NEW.state IN ('retiring','compromised')) OR
  (OLD.state='retiring' AND NEW.state IN ('retired','compromised')) OR
  (OLD.state='retired' AND NEW.state IN ('compromised','destroyed')) OR
  (OLD.state='compromised' AND NEW.state='destroyed')
 ) THEN
  RAISE EXCEPTION 'audit_key_registry_transition' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END;
$$ LANGUAGE plpgsql;
DROP TRIGGER IF EXISTS audit_key_registry_key_guard ON audit_key_registry_keys;
CREATE TRIGGER audit_key_registry_key_guard BEFORE INSERT OR UPDATE OR DELETE ON audit_key_registry_keys
 FOR EACH ROW EXECUTE FUNCTION audit_key_registry_guard_key();
DROP TRIGGER IF EXISTS audit_key_registry_key_truncate ON audit_key_registry_keys;
CREATE TRIGGER audit_key_registry_key_truncate BEFORE TRUNCATE ON audit_key_registry_keys
 FOR EACH STATEMENT EXECUTE FUNCTION audit_key_registry_guard_key();
CREATE OR REPLACE FUNCTION audit_key_registry_immutable() RETURNS trigger AS $$
BEGIN
 RAISE EXCEPTION 'audit_key_registry_immutable_history' USING ERRCODE='23514';
END;
$$ LANGUAGE plpgsql;
DROP TRIGGER IF EXISTS audit_key_registry_history_immutable ON audit_key_registry_history;
CREATE TRIGGER audit_key_registry_history_immutable BEFORE UPDATE OR DELETE ON audit_key_registry_history
 FOR EACH ROW EXECUTE FUNCTION audit_key_registry_immutable();
DROP TRIGGER IF EXISTS audit_key_registry_history_truncate ON audit_key_registry_history;
CREATE TRIGGER audit_key_registry_history_truncate BEFORE TRUNCATE ON audit_key_registry_history
 FOR EACH STATEMENT EXECUTE FUNCTION audit_key_registry_immutable();
DROP TRIGGER IF EXISTS audit_key_registry_operations_immutable ON audit_key_registry_operations;
CREATE TRIGGER audit_key_registry_operations_immutable BEFORE UPDATE OR DELETE ON audit_key_registry_operations
 FOR EACH ROW EXECUTE FUNCTION audit_key_registry_immutable();
DROP TRIGGER IF EXISTS audit_key_registry_operations_truncate ON audit_key_registry_operations;
CREATE TRIGGER audit_key_registry_operations_truncate BEFORE TRUNCATE ON audit_key_registry_operations
 FOR EACH STATEMENT EXECUTE FUNCTION audit_key_registry_immutable();
