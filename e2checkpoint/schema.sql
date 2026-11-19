-- 일반 E② v3의 별도 head 저장소. 설치 관리자가 epoch와 역할을 등록한다.
CREATE TABLE IF NOT EXISTS e2_http_audit_epochs (
    store_ref TEXT PRIMARY KEY,
    epoch_ref TEXT NOT NULL,
    generation BIGINT NOT NULL CHECK (generation > 0),
    anchor_digest BYTEA NOT NULL CHECK (octet_length(anchor_digest) = 32)
);
CREATE TABLE IF NOT EXISTS e2_http_audit_heads (
    store_ref TEXT NOT NULL,
    run_ref TEXT NOT NULL,
    epoch_ref TEXT NOT NULL,
    generation BIGINT NOT NULL CHECK (generation > 0),
    anchor_digest BYTEA NOT NULL CHECK (octet_length(anchor_digest) = 32),
    publisher_ref TEXT NOT NULL,
    key_ref TEXT NOT NULL,
    journal_id TEXT NOT NULL,
    revision BIGINT NOT NULL CHECK (revision > 0),
    phase TEXT NOT NULL CHECK (phase IN ('open', 'sealed')),
    checkpoint JSONB NOT NULL,
    snapshot_bytes BIGINT NOT NULL CHECK (snapshot_bytes > 0),
    snapshot_digest BYTEA NOT NULL CHECK (octet_length(snapshot_digest) = 32),
    PRIMARY KEY (store_ref, run_ref),
    UNIQUE (store_ref, journal_id)
);
