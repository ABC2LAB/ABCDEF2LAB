"""상태 차이 코어(평가 프로토콜 §8.2): 같은 컨테이너 pre·post SQLite 사본 비교."""

from __future__ import annotations

import io
import json
import os
import shutil
import sqlite3
import sys
import tarfile
import tempfile
import unittest
from pathlib import Path

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from state_diff import (  # noqa: E402
    RUN_KEY_BYTES, StateDiffError, diff, require_directory_root, runtime_diff, snapshot, tree_fingerprint,
)

KEY = bytes(range(RUN_KEY_BYTES))
SECRET = "hunter2-card-4111111111111111"


class _Db(unittest.TestCase):
    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.root = Path(self._tmp.name)
        self.path = self.root / "target.sqlite"
        conn = sqlite3.connect(self.path)
        conn.executescript(f"""
            CREATE TABLE Users (id INTEGER PRIMARY KEY AUTOINCREMENT, email TEXT, password TEXT, admin INTEGER);
            CREATE TABLE Feedbacks (id INTEGER PRIMARY KEY AUTOINCREMENT, comment TEXT, rating REAL, meta);
            CREATE TABLE Tags (product TEXT, tag TEXT, PRIMARY KEY (product, tag));
            CREATE TABLE Codes (code TEXT PRIMARY KEY, n INTEGER);
            CREATE TABLE Pairs (id INTEGER PRIMARY KEY, a TEXT, b TEXT);
            CREATE TABLE BasketItems (BasketId INTEGER, ProductId INTEGER, PRIMARY KEY (BasketId, ProductId));
            CREATE TABLE Logs (msg TEXT);
            INSERT INTO Users (email, password, admin) VALUES ('a@x', '{SECRET}', 0), ('b@x', 'p2', 1);
            INSERT INTO Feedbacks (comment, rating, meta) VALUES ('ok', 5.0, 1);
            INSERT INTO Tags VALUES ('p1', 't1'), ('p1', 't2');
            INSERT INTO Codes VALUES ('c1', 1);
            INSERT INTO Pairs VALUES (1, 'ab', 'sc');
            INSERT INTO BasketItems VALUES (1, 1), (1, 2);
            INSERT INTO Logs VALUES ('same');
        """)
        conn.commit()
        conn.close()
        self.pre = snapshot(self.path, KEY)

    def tearDown(self):
        self._tmp.cleanup()

    def change(self, sql: str) -> dict:
        conn = sqlite3.connect(self.path)
        conn.executescript(sql)
        conn.commit()
        conn.close()
        return diff(self.pre, snapshot(self.path, KEY))


class TestRows(_Db):
    def test_identical_copies_have_no_difference(self):
        result = diff(self.pre, snapshot(self.path, KEY))
        self.assertEqual((result["changed_tables"], result["content_changed"], result["trace_only"],
                          result["sequence_changed"]), ([], False, False, {}))
        self.assertEqual(result["tables"]["Users"]["rows_pre"], 2)

    def test_keyed_table_reports_added_removed_changed_ids(self):
        result = self.change("INSERT INTO Users (email, password, admin) VALUES ('c@x', 'p3', 0);"
                             "DELETE FROM Users WHERE id = 2; UPDATE Users SET admin = 1 WHERE id = 1;")
        users = result["tables"]["Users"]
        self.assertEqual((users["added"], users["removed"], users["changed"]), (1, 1, 1))
        self.assertEqual((users["added_ids"], users["removed_ids"], users["changed_ids"]), ([3], [2], [1]))
        self.assertEqual((users["rows_pre"], users["rows_post"], users["key"]), (2, 2, "id"))
        self.assertEqual(result["changed_tables"], ["Users"])
        self.assertTrue(result["content_changed"])

    def test_unkeyed_table_counts_multiset_changes_without_ids(self):
        result = self.change("DELETE FROM Tags WHERE tag = 't2'; INSERT INTO Tags VALUES ('p2', 't9'), ('p3', 't9');")
        tags = result["tables"]["Tags"]
        self.assertEqual((tags["key"], tags["added"], tags["removed"], tags["changed"], tags["added_ids"]),
                         (None, 2, 1, None, None))
        self.assertEqual((tags["rows_pre"], tags["rows_post"]), (2, 3))

    def test_value_boundaries_and_types_change_the_fingerprint(self):
        # 이어 붙인 문자열은 같고 열 경계만 옮긴 행, 형 친화성 없는 열에서 형식만 바꾼 값(정수 1 → 문자열 '1')도
        # 변경으로 잡힌다.
        # ('ab', 'sc') → ('abs', 'c')는 형식 표시만 붙여 이어 붙이면 같은 바이트가 된다. 길이 접두가 경계를 고정한다.
        result = self.change("UPDATE Pairs SET a = 'abs', b = 'c' WHERE id = 1;"
                             "UPDATE Feedbacks SET meta = '1' WHERE id = 1;")
        self.assertEqual(result["tables"]["Pairs"]["changed_ids"], [1])
        self.assertEqual(result["tables"]["Feedbacks"]["changed_ids"], [1])

    def test_composite_key_starting_with_an_integer_is_not_an_id(self):
        result = self.change("DELETE FROM BasketItems WHERE ProductId = 2;")
        items = result["tables"]["BasketItems"]
        self.assertEqual((items["key"], items["removed"], items["removed_ids"]), (None, 1, None))

    def test_unkeyed_duplicates_are_each_counted(self):
        result = self.change("INSERT INTO Logs VALUES ('same'), ('same');")
        self.assertEqual(result["tables"]["Logs"]["added"], 2)

    def test_removal_alone_marks_the_table_changed(self):
        result = self.change("DELETE FROM Users WHERE id = 2;")
        self.assertEqual((result["changed_tables"], result["content_changed"]), (["Users"], True))

    def test_single_text_key_is_counted_without_ids(self):
        # 정수 기본키(rowid 별칭)가 아닌 키는 id로 내보내지 않는다(값이 들어 있을 수 있다).
        result = self.change("UPDATE Codes SET n = 2 WHERE code = 'c1';")
        codes = result["tables"]["Codes"]
        self.assertEqual((codes["key"], codes["added"], codes["removed"], codes["changed_ids"]), (None, 1, 1, None))

    def test_schema_change_is_not_row_compared(self):
        result = self.change("ALTER TABLE Feedbacks ADD COLUMN extra TEXT;")
        feedback = result["tables"]["Feedbacks"]
        self.assertTrue(feedback["schema_changed"])
        self.assertEqual((feedback["added"], feedback["removed"], feedback["changed"]), (None, None, None))
        self.assertIn("Feedbacks", result["changed_tables"])

    def test_schema_objects_beyond_plain_columns_are_compared(self):
        # table_info는 생성 열을 빼고, type='table'만 보면 트리거·뷰·인덱스를 놓친다(#226 리뷰). 전부 상태 변경이다.
        generated = self.change("ALTER TABLE Pairs ADD COLUMN derived TEXT GENERATED ALWAYS AS (a || 'x') VIRTUAL;")
        self.assertTrue(generated["tables"]["Pairs"]["schema_changed"])
        self.assertEqual(generated["schema_objects"]["changed"], [["table", "Pairs"]])
        self.assertTrue(generated["content_changed"])
        for ddl, kind, name in (("CREATE TRIGGER trg AFTER INSERT ON Logs BEGIN DELETE FROM Logs; END;", "trigger", "trg"),
                                ("CREATE VIEW v AS SELECT * FROM Logs;", "view", "v"),
                                ("CREATE INDEX ix ON Logs(msg);", "index", "ix")):
            with self.subTest(kind=kind):
                self.pre = snapshot(self.path, KEY)  # 앞 단계 객체가 쌓인 상태에서 한 객체씩
                result = self.change(ddl)
                self.assertEqual(result["schema_objects"], {"added": [[kind, name]], "removed": [], "changed": []})
                self.assertEqual((result["changed_tables"], result["content_changed"], result["trace_only"]),
                                 ([], True, False))
                self.assertEqual(diff(snapshot(self.path, KEY), snapshot(self.path, KEY))["schema_objects"],
                                 {"added": [], "removed": [], "changed": []})
        self.pre = snapshot(self.path, KEY)
        dropped = self.change("DROP INDEX ix;")
        self.assertEqual(dropped["schema_objects"]["removed"], [["index", "ix"]])

    def test_generated_and_virtual_table_columns_are_read(self):
        conn = sqlite3.connect(self.path)
        conn.executescript("ALTER TABLE Pairs ADD COLUMN derived TEXT GENERATED ALWAYS AS (a || 'x') VIRTUAL;"
                           "CREATE VIRTUAL TABLE Docs USING fts5(body); INSERT INTO Docs VALUES ('one');")
        conn.commit()
        conn.close()
        self.pre = snapshot(self.path, KEY)
        self.assertIn(("derived", "TEXT", 2), self.pre["tables"]["Pairs"]["columns"])
        result = self.change("INSERT INTO Docs VALUES ('two');")
        self.assertEqual(result["tables"]["Docs"]["added"], 1)

    def test_new_object_with_sequence_is_not_trace_only(self):
        result = self.change("CREATE INDEX fx ON Feedbacks(comment);"
                             "INSERT INTO Feedbacks (comment, rating) VALUES ('tmp', 1.0);"
                             "DELETE FROM Feedbacks WHERE comment = 'tmp';")
        self.assertEqual((result["changed_tables"], result["content_changed"], result["trace_only"]), ([], True, False))

    def test_table_added_with_sequence_is_not_trace_only(self):
        result = self.change("CREATE TABLE Extra (id INTEGER PRIMARY KEY AUTOINCREMENT);"
                             "INSERT INTO Extra DEFAULT VALUES; DELETE FROM Extra;")
        self.assertEqual((result["tables_added"], result["content_changed"], result["trace_only"]),
                         (["Extra"], True, False))
        self.assertIn("Extra", result["sequence_changed"])

    def test_tables_added_and_removed(self):
        result = self.change("CREATE TABLE Extra (id INTEGER PRIMARY KEY); DROP TABLE Tags;")
        self.assertEqual((result["tables_added"], result["tables_removed"]), (["Extra"], ["Tags"]))
        self.assertTrue(result["content_changed"])
        self.assertNotIn("Tags", result["tables"])


class TestSignals(_Db):
    def test_signal_table_objects_stay_out_of_the_schema_difference(self):
        self.change("CREATE INDEX cix ON Codes(n);")
        result = diff(self.pre, snapshot(self.path, KEY), frozenset({"Codes"}))
        self.assertEqual((result["schema_objects"]["added"], result["content_changed"]), ([], False))

    def test_signal_table_is_counted_apart_from_the_state_difference(self):
        # 해결 기록 같은 신호 테이블의 변화는 상태 차이(판정 입력)에 들어가지 않고 건수만 따로 남는다.
        self.change("UPDATE Codes SET n = 2 WHERE code = 'c1';"
                             "INSERT INTO Feedbacks (comment, rating) VALUES ('tmp', 1.0);"
                             "DELETE FROM Feedbacks WHERE comment = 'tmp';")
        result = diff(self.pre, snapshot(self.path, KEY), frozenset({"Codes", "Feedbacks"}))
        self.assertEqual((result["changed_tables"], result["content_changed"], result["trace_only"],
                          result["sequence_changed"]), ([], False, False, {}))
        self.assertNotIn("Codes", result["tables"])
        self.assertEqual((result["signals"]["Codes"]["added"], result["signals"]["Codes"]["removed"]), (1, 1))
        self.assertEqual(result["signals"]["Feedbacks"]["rows_post"], 1)
        self.assertFalse(any(key.endswith("_ids") for key in result["signals"]["Feedbacks"]))

    def test_signal_tables_must_exist_and_be_a_frozenset_of_names(self):
        post = snapshot(self.path, KEY)
        for value, code in ((frozenset({"Nope"}), "state_signal_table_missing"),
                            ({"Codes"}, "state_signal_tables_invalid"),
                            (frozenset({1}), "state_signal_tables_invalid")):
            with self.subTest(value=value), self.assertRaisesRegex(StateDiffError, code):
                diff(self.pre, post, value)


class TestSequence(_Db):
    def test_create_then_delete_leaves_only_a_trace(self):
        # 보상(생성 → 삭제) 뒤 내용은 원복돼도 id 소비는 남는다(§8.2).
        result = self.change("INSERT INTO Feedbacks (comment, rating) VALUES ('tmp', 1.0);"
                             "DELETE FROM Feedbacks WHERE comment = 'tmp';")
        self.assertEqual(result["changed_tables"], [])
        self.assertFalse(result["content_changed"])
        self.assertTrue(result["trace_only"])
        self.assertEqual(result["sequence_changed"], {"Feedbacks": {"pre": 1, "post": 2}})

    def test_content_change_is_not_trace_only(self):
        result = self.change("INSERT INTO Feedbacks (comment, rating) VALUES ('kept', 1.0);")
        self.assertEqual((result["content_changed"], result["trace_only"]), (True, False))


class TestCopies(unittest.TestCase):
    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.root = Path(self._tmp.name)

    def tearDown(self):
        self._tmp.cleanup()

    def test_wal_copy_shows_the_last_commit(self):
        source = self.root / "src.sqlite"
        conn = sqlite3.connect(source)
        conn.execute("PRAGMA journal_mode=wal")
        conn.execute("PRAGMA wal_autocheckpoint=0")
        conn.execute("CREATE TABLE T (id INTEGER PRIMARY KEY, v TEXT)")
        conn.execute("INSERT INTO T (v) VALUES ('a')")
        conn.commit()
        copy = self.root / "copy"
        copy.mkdir()
        for name in os.listdir(self.root):
            if name.startswith("src.sqlite"):
                shutil.copy(self.root / name, copy / name)
        conn.close()
        self.assertTrue((copy / "src.sqlite-wal").exists())
        self.assertEqual(len(snapshot(copy / "src.sqlite", KEY)["tables"]["T"]["rows"]), 1)

    def test_bad_inputs_stop_with_fixed_codes(self):
        garbage = self.root / "garbage.sqlite"
        garbage.write_bytes(b"not a database" * 100)
        for path, key, code in ((self.root / "missing.sqlite", KEY, "state_copy_missing"),
                                (garbage, KEY, "state_copy_unreadable"),
                                (garbage, KEY[:-1], "state_run_key_invalid"),
                                (garbage, "k" * RUN_KEY_BYTES, "state_run_key_invalid")):
            with self.subTest(code=code), self.assertRaisesRegex(StateDiffError, code):
                snapshot(path, key)


class TestNoValues(_Db):
    def test_result_carries_counts_ids_and_names_only(self):
        result = self.change(f"UPDATE Users SET password = '{SECRET}x' WHERE id = 1;")
        text = json.dumps(result)
        self.assertNotIn(SECRET, text)
        self.assertNotIn("a@x", text)
        for table in self.pre["tables"].values():
            fingerprints = table["rows"].values() if table["key"] else table["rows"]
            for fp in fingerprints:
                self.assertNotIn(fp.hex(), text)
        for _, fp in self.pre["objects"].values():
            self.assertNotIn(fp.hex(), text)
        self.assertNotIn("CREATE", text)  # 정의문(DDL)은 내보내지 않는다

    def test_fingerprints_depend_on_the_run_key(self):
        # 키가 다르면 같은 행·정의도 지문이 다르다. 그래서 pre·post는 같은 회차 키로 떠야 한다.
        other = snapshot(self.path, bytes(RUN_KEY_BYTES))
        self.assertNotEqual(self.pre["tables"]["Users"]["rows"][1], other["tables"]["Users"]["rows"][1])
        result = diff(self.pre, other)
        self.assertIn(["table", "Users"], result["schema_objects"]["changed"])
        self.assertTrue(result["tables"]["Users"]["schema_changed"])


def tree(*members) -> bytes:
    """(이름, 내용). 내용이 None이면 디렉터리, 'link:' 접두면 심볼릭 링크. 끝의 dict는 TarInfo 속성 덮어쓰기."""
    out = io.BytesIO()
    with tarfile.open(fileobj=out, mode="w") as archive:
        for name, body, *rest in members:
            info = tarfile.TarInfo(name)
            for attribute, value in (rest[0] if rest else {}).items():
                setattr(info, attribute, value)
            if body is None:
                info.type = tarfile.DIRTYPE
                archive.addfile(info)
            elif body.startswith(b"link:"):
                info.type, info.linkname = tarfile.SYMTYPE, body[5:].decode()
                archive.addfile(info)
            else:
                info.size = len(body)
                archive.addfile(info, io.BytesIO(body))
    return out.getvalue()


def fp(*members):
    return tree_fingerprint(tree(*members), KEY)


BASE = (("ftp", None), ("ftp/a.md", b"one"), ("ftp/b.md", b"two"))


class TestRuntimeTrees(unittest.TestCase):
    def compare(self, *post):
        return runtime_diff({"/app/ftp": fp(*BASE)}, {"/app/ftp": fp(*post)})

    def test_same_tree_has_no_difference(self):
        result = self.compare(*BASE)
        self.assertFalse(result["runtime_changed"])
        self.assertEqual(result["runtime"]["/app/ftp"], {"entries_pre": 3, "entries_post": 3, "missing_post": False,
                                                         "added": 0, "removed": 0, "changed": 0})

    def test_added_removed_and_changed_are_each_counted_and_flagged(self):
        for post, field, count in (((*BASE, ("ftp/order.pdf", b"x")), "added", 1),
                                   (BASE[:2], "removed", 1),
                                   ((BASE[0], BASE[1], ("ftp/b.md", b"TWO")), "changed", 1)):
            with self.subTest(field=field):
                result = self.compare(*post)
                self.assertEqual(result["runtime"]["/app/ftp"][field], count)
                self.assertTrue(result["runtime_changed"])

    def test_metadata_except_time_changes_the_fingerprint(self):
        for attributes, label in (({"mode": 0o600}, "mode"), ({"uid": 7}, "uid"), ({"gid": 7}, "gid")):
            with self.subTest(label=label):
                result = self.compare(BASE[0], BASE[1], ("ftp/b.md", b"two", attributes))
                self.assertEqual(result["runtime"]["/app/ftp"]["changed"], 1)
        # 내용이 그대로면 시각만 바뀐 건 상태 차이가 아니다.
        touched = self.compare(BASE[0], BASE[1], ("ftp/b.md", b"two", {"mtime": 1234567}))
        self.assertFalse(touched["runtime_changed"])

    def test_kind_and_link_target_change_the_fingerprint(self):
        self.assertEqual(self.compare(BASE[0], BASE[1], ("ftp/b.md", b"link:two"))["runtime"]["/app/ftp"]["changed"], 1)
        links = runtime_diff({"/r": fp(("l", b"link:x"))}, {"/r": fp(("l", b"link:y"))})
        self.assertEqual(links["runtime"]["/r"]["changed"], 1)

    def test_root_gone_after_the_run_counts_every_entry_as_removed(self):
        result = runtime_diff({"/app/ftp": fp(*BASE)}, {"/app/ftp": None})
        self.assertEqual(result["runtime"]["/app/ftp"], {"entries_pre": 3, "entries_post": 0, "missing_post": True,
                                                         "added": 0, "removed": 3, "changed": 0})
        self.assertTrue(result["runtime_changed"])
        # 원래 비어 있던 일은 없다(tar에 디렉터리 항목이 있다). 그래도 사라짐 자체가 변화인지 단독으로 본다.
        self.assertTrue(runtime_diff({"/r": {}}, {"/r": None})["runtime_changed"])

    def test_roots_must_match_and_pre_cannot_be_missing(self):
        for pre, post in (({"/a": {}}, {"/b": {}}), ({"/a": {}}, {}), ({}, {"/a": {}}), ({"/a": None}, {"/a": {}})):
            with self.subTest(pre=pre, post=post), self.assertRaisesRegex(StateDiffError, "state_runtime_roots_mismatch"):
                runtime_diff(pre, post)
        self.assertEqual(runtime_diff({}, {}), {"runtime": {}, "runtime_changed": False})

    def test_bad_archives_stop_with_a_fixed_code(self):
        duplicate = tree(("x", b"1"), ("x", b"2"))
        whole = tree(("x", b"0123456789" * 200))
        truncated = whole[:600]  # 헤더는 온전하고 내용이 잘린 tar
        for archive, label in ((duplicate, "duplicate"), (truncated, "truncated"), (b"not a tar" * 100, "garbage"),
                               (b"", "empty"), (tree(), "no entries")):
            with self.subTest(label=label), self.assertRaisesRegex(StateDiffError, "state_tree_invalid"):
                tree_fingerprint(archive, KEY)
        for key in (KEY[:-1], "k" * RUN_KEY_BYTES):
            with self.assertRaisesRegex(StateDiffError, "state_run_key_invalid"):
                tree_fingerprint(tree(("x", b"1")), key)

    def test_directory_root_must_be_the_requested_entry_and_a_real_directory(self):
        require_directory_root(tree(("ftp", None), ("ftp/a", b"1")), "/app/ftp")
        for archive, label in ((tree(("ftp", b"link:/data/ftp")), "link"), (tree(("ftp", b"1")), "regular"),
                               (tree(("other", None), ("ftp", b"1")), "another directory"),
                               (tree(("nested/ftp", None)), "same name below another entry")):
            with self.subTest(label=label), self.assertRaisesRegex(StateDiffError, "state_tree_root_not_directory"):
                require_directory_root(archive, "/app/ftp")
        for archive, label in ((b"not a tar" * 100, "garbage"), (b"", "empty")):
            with self.subTest(label=label), self.assertRaisesRegex(StateDiffError, "state_tree_invalid"):
                require_directory_root(archive, "/app/ftp")

    def test_fingerprints_depend_on_the_key_and_output_has_no_names_or_values(self):
        self.assertNotEqual(fp(("x", b"secret"))["x"], tree_fingerprint(tree(("x", b"secret")), bytes(RUN_KEY_BYTES))["x"])
        pre = {"/app/ftp": fp(("ftp/order_77.pdf", b"secret-body"))}
        text = json.dumps(runtime_diff(pre, {"/app/ftp": fp(("ftp/order_88.pdf", b"secret-body-2"))}))
        for leaked in ("order_77", "order_88", "secret", pre["/app/ftp"]["ftp/order_77.pdf"].hex()):
            self.assertNotIn(leaked, text)


if __name__ == "__main__":
    unittest.main()
