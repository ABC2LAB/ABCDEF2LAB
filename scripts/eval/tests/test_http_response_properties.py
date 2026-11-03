"""HTTP 응답 framing·wire 상한의 유한 fuzz 회귀.

실제 소켓·서버·파일 없이 HTTPResponse와 메모리 stream만 사용한다. 이 reader의
책임은 body bytes와 framing 검증까지이며 JSON 의미·GT·해결 귀속은 검사하지 않는다.
"""

from __future__ import annotations

import io
import itertools
import os
import sys
import unittest
from unittest.mock import patch
from http.client import HTTPResponse

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

import snapshot_collector as collector

_MARKER = b"SYNTHETIC-PRIVATE-MARKER"
_BODY = ("합성 body / UTF-8 / ".encode("utf-8") + bytes(range(16)) + " / 끝".encode("utf-8"))
_SHORT_BODY = "A한\nZ\x00!".encode("utf-8")


class _Socket:
    def __init__(self, wire: bytes):
        self.wire = wire
        self.stream = None

    def makefile(self, *_args, **_kwargs):
        self.stream = io.BytesIO(self.wire)
        return self.stream

    def close(self):
        pass


class _TrackingStream(io.BytesIO):
    def __init__(self, value: bytes):
        super().__init__(value)
        self.read_sizes = []

    def read(self, size=-1):
        self.read_sizes.append(size)
        return super().read(size)


class _TrackingSocket(_Socket):
    def makefile(self, *_args, **_kwargs):
        self.stream = _TrackingStream(self.wire)
        return self.stream


def _chunk_wire(body: bytes, sizes: tuple[int, ...], *, upper: bool = False,
                extensions: bool = False) -> bytes:
    if not body:
        assert sizes in ((), (0,))
        return b"0\r\n\r\n"
    chunks = []
    offset = 0
    for index, size in enumerate(sizes):
        part = body[offset:offset + size]
        offset += size
        rendered = format(size, "X" if upper else "x")
        if extensions:
            rendered += f";synthetic={index}"
        chunks.append(rendered.encode("ascii") + b"\r\n" + part + b"\r\n")
    assert offset == len(body)
    return b"".join(chunks) + b"0\r\n\r\n"


def _wire(body: bytes, *, framing: str, declared: int | None = None,
          headers: tuple[tuple[str, str], ...] = (), trailing: bytes = b"",
          chunk_sizes: tuple[int, ...] | None = None, upper: bool = False,
          extensions: bool = False) -> bytes:
    header_map = {"Content-Type": "application/json"}
    header_map.update(headers)
    base = list(header_map.items())
    if framing == "length":
        base.append(("Content-Length", str(len(body) if declared is None else declared)))
        payload = body + trailing
    elif framing == "eof":
        payload = body + trailing
    elif framing == "chunked":
        base.append(("Transfer-Encoding", "chunked"))
        sizes = chunk_sizes or (len(body),)
        payload = _chunk_wire(body, sizes, upper=upper, extensions=extensions) + trailing
    else:
        raise AssertionError(framing)
    return (b"HTTP/1.1 200 OK\r\n" + b"".join(name.encode() + b": " + value.encode() + b"\r\n"
                                                for name, value in base) + b"\r\n" + payload)


def _read(wire: bytes, maximum: int, *, tracking: bool = False):
    sock = _TrackingSocket(wire) if tracking else _Socket(wire)
    response = HTTPResponse(sock)
    response.begin()
    return collector._read_response(response, maximum), sock


def _positive_partitions(length: int):
    """All positive compositions for short bodies; bounded representatives otherwise."""
    if length <= 8:
        for mask in range(1 << (length - 1)):
            cuts = [index for index in range(length - 1) if mask & (1 << index)]
            points = [0, *[cut + 1 for cut in cuts], length]
            yield tuple(right - left for left, right in zip(points, points[1:]))
        return
    yield (length,)
    for cut in range(1, length):
        yield (cut, length - cut)
    yield (1, 2, length - 3)
    yield (2, length - 3, 1)


class TestHTTPResponseProperties(unittest.TestCase):
    def setUp(self):
        for target in ("socket.socket", "socket.create_connection", "subprocess.Popen", "os.system"):
            guard = patch(target, side_effect=AssertionError("HTTP framing properties are offline"))
            guard.start()
            self.addCleanup(guard.stop)

    def assert_rejected(self, wire: bytes, maximum: int):
        before = bytes(wire)
        with self.assertRaises(collector.SnapshotCollectionError) as caught:
            _read(wire, maximum)
        self.assertEqual(wire, before)
        self.assertNotIn("SYNTHETIC-PRIVATE-MARKER", str(caught.exception))

    def test_content_length_and_eof_return_each_declared_body_once(self):
        for body in (b"", _SHORT_BODY, _BODY):
            for framing in ("length", "eof"):
                wire = _wire(body, framing=framing)
                caps = (1, 2) if not body else (len(body),)
                for cap in caps:
                    with self.subTest(body_len=len(body), framing=framing, cap=cap):
                        self.assertEqual(_read(wire, cap)[0], body)

    def test_empty_chunk_terminator_is_single_and_cap_bound(self):
        wire = _wire(b"", framing="chunked")
        self.assertEqual(wire.split(b"\r\n\r\n", 1)[1], b"0\r\n\r\n")
        cap = len(b"0\r\n\r\n")
        self.assert_rejected(wire, cap - 1)
        for requested_cap in (cap, cap + 1):
            with self.subTest(cap=requested_cap):
                self.assertEqual(_read(wire, requested_cap)[0], b"")

    def test_chunk_byte_partitions_hex_and_extensions_preserve_decoded_body(self):
        for body in (_SHORT_BODY, _BODY):
            partitions = tuple(_positive_partitions(len(body)))
            for sizes, upper, extensions in itertools.product(partitions, (False, True), (False, True)):
                wire = _wire(body, framing="chunked", chunk_sizes=sizes,
                             upper=upper, extensions=extensions)
                with self.subTest(body_len=len(body), sizes=sizes, upper=upper,
                                  extensions=extensions):
                    self.assertEqual(_read(wire, len(wire.split(b"\r\n\r\n", 1)[1]))[0], body)

    def test_wire_cap_is_inclusive_for_each_framing_and_bounded_read_is_requested(self):
        chunked = _chunk_wire(_BODY, (5, len(_BODY) - 5))
        cases = (
            (_wire(_BODY, framing="length"), len(_BODY)),
            (_wire(_BODY, framing="eof"), len(_BODY)),
            (_wire(_BODY, framing="chunked", chunk_sizes=(5, len(_BODY) - 5)), len(chunked)),
        )
        for wire, cap in cases:
            for requested_cap, expected in ((cap - 1, False), (cap, True), (cap + 1, True)):
                with self.subTest(cap=cap, requested_cap=requested_cap, wire_len=len(wire)):
                    if expected:
                        actual, sock = _read(wire, requested_cap, tracking=True)
                        self.assertEqual(actual, _BODY)
                        self.assertIn(requested_cap + 1, sock.stream.read_sizes)
                        self.assertNotIn(_MARKER, actual)
                    else:
                        self.assert_rejected(wire, requested_cap)

    def test_complete_chunk_wire_every_proper_prefix_is_rejected(self):
        complete = _wire(_SHORT_BODY, framing="chunked", chunk_sizes=(2, 1, 3, 2),
                          upper=True, extensions=True)
        header, payload = complete.split(b"\r\n\r\n", 1)
        for end in range(0, len(payload)):
            wire = header + b"\r\n\r\n" + payload[:end]
            with self.subTest(prefix=end):
                self.assert_rejected(wire, len(payload))

    def test_content_length_body_prefixes_fail_but_eof_prefixes_are_valid_bodies(self):
        for end in range(len(_SHORT_BODY)):
            prefix = _SHORT_BODY[:end]
            with self.subTest(prefix=end):
                self.assert_rejected(_wire(prefix, framing="length", declared=len(_SHORT_BODY)),
                                     len(_SHORT_BODY))
                self.assertEqual(_read(_wire(prefix, framing="eof"), max(1, len(prefix)))[0], prefix)

    def test_malformed_status_headers_lengths_and_chunk_framing_fail_closed(self):
        cases = (
            b"HTTP/1.1 503 Service Unavailable\r\nContent-Type: application/json\r\n"
            b"Content-Length: 0\r\n\r\n",
            _wire(_BODY, framing="length", headers=(("Content-Type", "text/plain"),)),
            _wire(_BODY, framing="length", headers=(("Content-Encoding", "gzip"),)),
            _wire(_BODY, framing="length", declared=len(_BODY) + 1),
            _wire(_BODY, framing="length", declared=len(_BODY), trailing=b"tail"),
            b"HTTP/1.1 200 OK\r\nContent-Type: application/json\r\n"
            b"Content-Length: nope\r\n\r\n" + _BODY,
            b"HTTP/1.1 200 OK\r\nContent-Type: application/json\r\n"
            b"Content-Length: 1\r\nTransfer-Encoding: chunked\r\n\r\na",
        )
        malformed_chunks = (
            b"g\r\nabc\r\n0\r\n\r\n", b"3\r\nabc!\r\n0\r\n\r\n",
            b"3\r\nabc\r\n", b"0\r\nX-Synthetic: value\r\n\r\n",
            b"4\r\nabc\r\n0\r\n\r\n", b"3\r\nabc\n0\r\n\r\n",
        )
        for wire in malformed_chunks:
            cases += ((b"HTTP/1.1 200 OK\r\nContent-Type: application/json\r\n"
                       b"Transfer-Encoding: chunked\r\n\r\n" + wire),)
        for body in (_SHORT_BODY, _BODY[:8]):
            for tail in (b"X", b"\r\n", _MARKER):
                cases += (_wire(body, framing="length", declared=len(body), trailing=tail),)
                chunked = _wire(body, framing="chunked", trailing=tail)
                cases += (chunked,)
            chunked = _wire(body, framing="chunked")
            header, payload = chunked.split(b"\r\n\r\n", 1)
            first_line_end = payload.index(b"\r\n")
            size = payload[:first_line_end]
            data_start = first_line_end + 2
            wrong_size = format(len(body) + 1, "x").encode("ascii")
            cases += (header + b"\r\n\r\n" + wrong_size + payload[first_line_end:],)
            cases += (header + b"\r\n\r\n" + size + b"\n" + payload[data_start:],)
        for wire in cases:
            with self.subTest(wire=wire[:36]):
                self.assert_rejected(wire, max(len(wire), len(_BODY) + 32))

    def test_regression_assertions_detect_cap_trailing_and_chunk_assembly_weakening(self):
        oversized = _wire(_BODY + b"X", framing="length", declared=len(_BODY) + 1)
        self.assert_rejected(oversized, len(_BODY))
        original_reader = collector._read_response
        with patch.object(collector, "_read_response",
                          side_effect=lambda response, maximum: original_reader(response, maximum + 1)):
            with self.assertRaises(AssertionError):
                self.assert_rejected(oversized, len(_BODY))

        trailing = _wire(_BODY, framing="length", declared=len(_BODY), trailing=_MARKER)
        self.assert_rejected(trailing, len(_BODY) + len(_MARKER))

        def ignore_trailing(response, maximum):
            # 약화된 구현이 declared length까지만 읽고 EOF 뒤 바이트를 보지 않는 경우.
            return response.fp.read(len(_BODY))

        with patch.object(collector, "_read_response", side_effect=ignore_trailing):
            with self.assertRaises(AssertionError):
                self.assert_rejected(trailing, len(_BODY) + len(_MARKER))

        chunked = _chunk_wire(_BODY, (len(_BODY),))
        wire = (b"HTTP/1.1 200 OK\r\nContent-Type: application/json\r\n"
                b"Transfer-Encoding: chunked\r\n\r\n" + chunked)
        self.assertEqual(_read(wire, len(chunked))[0], _BODY)
        with patch.object(collector, "_decode_chunks", return_value=b""):
            with self.assertRaises(AssertionError):
                self.assertEqual(_read(wire, len(chunked))[0], _BODY)


if __name__ == "__main__":
    unittest.main()
