"""대상 저장소 상태 차이 — SQLite 사본 두 개 비교 코어(평가 프로토콜 §8.1·§8.2).

러너가 격리 구간 밖에서 뜬 **같은 컨테이너**의 pre·post SQLite 사본을 받아 테이블별로 행이 얼마나 바뀌었는지 센다.
사본 뜨기(대상 일시정지·복사)와 앱별 경로 설정은 이 모듈 밖(러너 어댑터)이다. 여기는 파일 두 개만 본다.

- 값은 내보내지 않는다. 결과에는 테이블·열 이름, 건수, 정수 id, sqlite_sequence 값(숫자)만 들어간다.
  저장소에는 비밀번호 해시·카드 번호 같은 값이 있다(§8.2).
- 행 비교는 **회차 키**(러너 메모리에만 두는 난수)로 HMAC한 행 지문으로 한다. 지문은 pre·post 대조에만 쓰고 결과에
  넣지 않는다. 키 없이 평문 해시를 쓰면 값이 몇 가지뿐인 열(불리언·역할 등)을 지문에서 거꾸로 맞출 수 있다.
- 정수 기본키 열이 하나인 테이블(SQLite rowid 별칭)은 그 id로 행을 맞춰 추가·삭제·변경을 id와 함께 센다.
  그 밖의 테이블은 지문 다중집합의 증감만 센다. 이때 '변경'은 삭제+추가와 구분되지 않으므로 null로 둔다.
- 열 구성이 바뀐 테이블은 행을 맞대지 않는다(schema_changed, 행 단위 건수 null). 같은 컨테이너 안에서는 없어야 하는
  일이라 조용히 비교하지 않는다.
- sqlite_sequence는 행 비교와 별도로 테이블별 seq 값을 그대로 비교한다. 만들었다 지워 내용이 원복돼도 id 소비 흔적은
  남는다(§8.2 "내용은 원복, 흔적은 남음").
- WAL 모드 DB는 `-wal` 파일까지 같이 복사해야 마지막 커밋이 보인다. `immutable=1`로 열면 WAL을 무시하므로 쓰지 않고
  읽기 전용(`mode=ro`)으로 연다. 사본 디렉터리는 러너 전용이라 SQLite가 `-shm`을 만들어도 대상에는 영향이 없다.

- 앱이 챌린지 해결 기록을 같은 DB에 두는 경우(Juice Shop v15의 `Challenges`) 그 테이블은 `signal_tables`로 넘겨
  상태 차이와 따로 센다. 해결 기록은 '해결 신호'이지 상태 차이 판정 입력이 아니다(§8.2, 절대규칙 7). 테이블 이름은
  러너 어댑터 설정에서 받고 이 모듈에 박지 않는다.

이 결과는 '관측 가능한 대상 상태의 변화'일 뿐이다. 메모리 저장(리뷰·주문)·런타임 파일·외부 호출은 여기서 보지 않는다.
"""

from __future__ import annotations

import hashlib
import hmac
import sqlite3
import struct
from collections import Counter
from pathlib import Path

RUN_KEY_BYTES = 32
SEQUENCE_TABLE = "sqlite_sequence"


class StateDiffError(ValueError):
    """사본을 읽을 수 없거나 비교 전제가 깨졌다. 고정 코드만 담는다(경로·값 없음)."""


def _encode(value: object) -> bytes:
    # 형식 표시 + 길이 접두로 열 경계를 고정한다(문자열 'ab'+'c'와 'a'+'bc'가 같은 지문이 되지 않게).
    if value is None:
        return b"n"
    if isinstance(value, int):
        body, tag = str(value).encode(), b"i"
    elif isinstance(value, float):
        body, tag = value.hex().encode(), b"f"
    elif isinstance(value, str):
        body, tag = value.encode("utf-8", "surrogatepass"), b"s"
    elif isinstance(value, bytes):
        body, tag = value, b"b"
    else:
        raise StateDiffError("state_value_type_unknown")
    return tag + struct.pack(">Q", len(body)) + body


def _fingerprint(key: bytes, values: tuple) -> bytes:
    return hmac.new(key, b"".join(_encode(v) for v in values), hashlib.sha256).digest()


def _quote(name: str) -> str:
    return '"' + name.replace('"', '""') + '"'


def _open(path: Path) -> sqlite3.Connection:
    if not path.is_file():
        raise StateDiffError("state_copy_missing")
    try:
        conn = sqlite3.connect(path.resolve().as_uri() + "?mode=ro", uri=True)
        conn.execute("SELECT count(*) FROM sqlite_master").fetchone()
    except sqlite3.DatabaseError:
        raise StateDiffError("state_copy_unreadable") from None
    return conn


def snapshot(path: str | Path, key: bytes) -> dict:
    """SQLite 사본 하나의 테이블별 열 구성·행 지문·sqlite_sequence 값. 지문은 비교에만 쓰고 내보내지 않는다."""
    if not isinstance(key, bytes) or len(key) != RUN_KEY_BYTES:
        raise StateDiffError("state_run_key_invalid")
    conn = _open(Path(path))
    try:
        names = [row[0] for row in conn.execute(
            "SELECT name FROM sqlite_master WHERE type = 'table' ORDER BY name")]
        tables: dict[str, dict] = {}
        sequence: dict[str, int] = {}
        for name in names:
            if name == SEQUENCE_TABLE:
                for table, seq in conn.execute(f"SELECT name, seq FROM {_quote(SEQUENCE_TABLE)}"):
                    if not isinstance(table, str) or type(seq) is not int or table in sequence:
                        raise StateDiffError("state_sequence_invalid")
                    sequence[table] = seq
                continue
            info = conn.execute(f"PRAGMA table_info({_quote(name)})").fetchall()
            columns = [(row[1], row[2]) for row in info]
            pk = [row for row in info if row[5] > 0]
            # 정수 기본키 열 하나(rowid 별칭)만 행 id로 쓴다. 복합·문자열 키는 지문 다중집합으로 센다.
            key_column = pk[0][1] if len(pk) == 1 and pk[0][2].upper() == "INTEGER" else None
            select = ", ".join(_quote(c) for c, _ in columns)
            rows = conn.execute(f"SELECT {select} FROM {_quote(name)}")
            if key_column is None:
                tables[name] = {"columns": columns, "key": None,
                                "rows": Counter(_fingerprint(key, tuple(r)) for r in rows)}
                continue
            index = [c for c, _ in columns].index(key_column)
            keyed: dict[int, bytes] = {}
            for r in rows:
                row_id = r[index]
                if type(row_id) is not int or row_id in keyed:
                    raise StateDiffError("state_row_id_invalid")
                keyed[row_id] = _fingerprint(key, tuple(r))
            tables[name] = {"columns": columns, "key": key_column, "rows": keyed}
    except sqlite3.DatabaseError:
        raise StateDiffError("state_copy_unreadable") from None
    finally:
        conn.close()
    return {"tables": tables, "sequence": sequence}


def _row_count(table: dict) -> int:
    return sum(table["rows"].values()) if table["key"] is None else len(table["rows"])


def _table_diff(before: dict, after: dict) -> dict:
    rows_pre, rows_post = _row_count(before), _row_count(after)
    result = {"key": before["key"], "rows_pre": rows_pre, "rows_post": rows_post, "schema_changed": False,
              "added": None, "removed": None, "changed": None,
              "added_ids": None, "removed_ids": None, "changed_ids": None}
    if before["columns"] != after["columns"] or before["key"] != after["key"]:
        result["schema_changed"] = True
        return result
    if before["key"] is None:
        grown = after["rows"] - before["rows"]
        shrunk = before["rows"] - after["rows"]
        result.update(added=sum(grown.values()), removed=sum(shrunk.values()))
        return result
    pre, post = before["rows"], after["rows"]
    added = sorted(post.keys() - pre.keys())
    removed = sorted(pre.keys() - post.keys())
    changed = sorted(i for i in pre.keys() & post.keys() if pre[i] != post[i])
    result.update(added=len(added), removed=len(removed), changed=len(changed),
                  added_ids=added, removed_ids=removed, changed_ids=changed)
    return result


def diff(pre: dict, post: dict, signal_tables: frozenset[str] = frozenset()) -> dict:
    """같은 컨테이너 pre·post 사본 지문의 차이. 건수·정수 id·테이블·열 이름·seq 값만 담는다.

    `signal_tables`(해결 기록 등)는 `signals`에 건수만 따로 두고 상태 차이 판정(changed_tables·content_changed·
    sequence_changed)에서 뺀다. 이름이 사본에 없으면 설정 오류로 멈춘다(조용히 판정에 섞이지 않게).
    """
    if not isinstance(signal_tables, frozenset) or not all(isinstance(name, str) for name in signal_tables):
        raise StateDiffError("state_signal_tables_invalid")
    if not signal_tables <= (set(pre["tables"]) & set(post["tables"])):
        raise StateDiffError("state_signal_table_missing")
    signals = {name: {key: value for key, value in _table_diff(pre["tables"][name], post["tables"][name]).items()
                      if not key.endswith("_ids")}
               for name in sorted(signal_tables)}
    names_pre, names_post = set(pre["tables"]) - signal_tables, set(post["tables"]) - signal_tables
    tables = {name: _table_diff(pre["tables"][name], post["tables"][name])
              for name in sorted(names_pre & names_post)}
    sequence = {}
    for table in sorted((set(pre["sequence"]) | set(post["sequence"])) - signal_tables):
        before, after = pre["sequence"].get(table), post["sequence"].get(table)
        if before != after:
            sequence[table] = {"pre": before, "post": after}
    changed_tables = sorted(name for name, t in tables.items()
                            if t["schema_changed"] or t["added"] or t["removed"] or t["changed"])
    return {
        "tables": tables,
        "signals": signals,
        "tables_added": sorted(names_post - names_pre),
        "tables_removed": sorted(names_pre - names_post),
        "sequence_changed": sequence,
        "changed_tables": changed_tables,
        # 행 내용·테이블 구성·id 소비 중 하나라도 바뀌었으면 차이 있음. seq만 바뀌면 '내용은 원복, 흔적은 남음'.
        "content_changed": bool(changed_tables or names_pre != names_post),
        "trace_only": not changed_tables and names_pre == names_post and bool(sequence),
    }
