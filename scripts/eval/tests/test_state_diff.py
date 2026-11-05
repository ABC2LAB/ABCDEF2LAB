"""상태 차이 코어(평가 프로토콜 §8.2): 같은 컨테이너 pre·post SQLite 사본 비교."""

from __future__ import annotations

import json
import os
import shutil
import sqlite3
import sys
import tempfile
import unittest
from pathlib import Path

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from state_diff import RUN_KEY_BYTES, StateDiffError, diff, snapshot  # noqa: E402

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

    def test_tables_added_and_removed(self):
        result = self.change("CREATE TABLE Extra (id INTEGER PRIMARY KEY); DROP TABLE Tags;")
        self.assertEqual((result["tables_added"], result["tables_removed"]), (["Extra"], ["Tags"]))
        self.assertTrue(result["content_changed"])
        self.assertNotIn("Tags", result["tables"])


class TestSignals(_Db):
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

    def test_fingerprints_depend_on_the_run_key(self):
        # 키가 다르면 같은 행도 지문이 다르다. 그래서 pre·post는 같은 회차 키로 떠야 한다.
        other = snapshot(self.path, bytes(RUN_KEY_BYTES))
        self.assertNotEqual(self.pre["tables"]["Users"]["rows"][1], other["tables"]["Users"]["rows"][1])
        self.assertEqual(diff(self.pre, other)["tables"]["Users"]["changed_ids"], [1, 2])


if __name__ == "__main__":
    unittest.main()
