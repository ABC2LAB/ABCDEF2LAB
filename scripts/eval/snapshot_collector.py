"""독립 평가 계측: 설정된 loopback HTTP 조회의 원본 body를 스냅샷 저장기에 전달.

엔진 요청·LLM·사용자 인증정보와 분리한다. 지원하지 않는 경로/HTTPS는 명시적 오류이며,
리다이렉트·환경 프록시·자동 재시도는 사용하지 않는다.
"""

from __future__ import annotations

import http.client
import ipaddress
import json
import math
import os
import sys
from contextlib import closing
from dataclasses import dataclass
from datetime import datetime, timezone
from pathlib import Path
from urllib.parse import urlsplit

from snapshot_writer import SnapshotArtifacts, SnapshotContext, SnapshotWriteError, write_snapshot


class SnapshotCollectionError(ValueError):
    """계측 설정·수집·저장 실패. 응답 body·헤더값은 오류에 담지 않는다."""


@dataclass(frozen=True)
class SnapshotEndpoint:
    origin: str
    path: str
    timeout_seconds: float
    max_bytes: int


@dataclass(frozen=True)
class CollectedSnapshot:
    artifacts: SnapshotArtifacts
    requested_at: str
    received_at: str


def _endpoint(config: SnapshotEndpoint) -> tuple[str, int, str]:
    if not isinstance(config, SnapshotEndpoint):
        raise SnapshotCollectionError("SnapshotEndpoint 설정 필요")
    for value in (config.origin, config.path):
        if not isinstance(value, str) or not value or any(ord(char) <= 32 or ord(char) == 127 for char in value):
            raise SnapshotCollectionError("origin/path 문자열 부정")
    if type(config.max_bytes) is not int or not 0 < config.max_bytes < sys.maxsize:
        raise SnapshotCollectionError("max_bytes는 양의 정수여야 함")
    try:
        valid_timeout = (type(config.timeout_seconds) in (int, float)
                         and math.isfinite(config.timeout_seconds) and config.timeout_seconds > 0)
    except OverflowError:
        valid_timeout = False
    if not valid_timeout:
        raise SnapshotCollectionError("timeout_seconds는 양의 유한수여야 함")
    try:
        origin = urlsplit(config.origin)
        host, port = origin.hostname, origin.port
        if (origin.scheme != "http" or not host or origin.username is not None or origin.password is not None
                or origin.path not in ("", "/") or origin.query or origin.fragment):
            raise SnapshotCollectionError("HTTP origin만 허용(인증정보/경로/query/fragment 금지)")
        if "?" in config.origin or "#" in config.origin or "%" in host:
            raise SnapshotCollectionError("origin 부정")
        port = 80 if port is None else port
        if not 1 <= port <= 65535:
            raise SnapshotCollectionError("origin port 부정")
        if host.lower() == "localhost":
            connect_host = "127.0.0.1"
        else:
            address = ipaddress.ip_address(host)
            if not address.is_loopback:
                raise SnapshotCollectionError("loopback origin만 허용")
            connect_host = str(address)
        path = urlsplit(config.path)
        if (not config.path.startswith("/") or config.path.startswith("//") or "\\" in config.path
                or path.scheme or path.netloc or "?" in config.path or "#" in config.path or not config.path.isascii()):
            raise SnapshotCollectionError("설정된 절대 조회 path만 허용(query/fragment 금지)")
    except SnapshotCollectionError:
        raise
    except ValueError:
        raise SnapshotCollectionError("origin/path 설정 검증 실패") from None
    host_header = f"[{host}]:{port}" if ":" in host else f"{host}:{port}"
    return connect_host, port, host_header


def _output_ready(run_directory: str | Path, phase: str, context: SnapshotContext) -> None:
    if phase not in ("pre", "post") or not isinstance(context, SnapshotContext):
        raise SnapshotCollectionError("pre/post와 SnapshotContext 필요")
    for value in vars(context).values():
        if not isinstance(value, str) or not value.strip():
            raise SnapshotCollectionError("회차 신원 문자열 부정")
        try:
            value.encode("utf-8")
        except UnicodeError:
            raise SnapshotCollectionError("UTF-8 회차 신원 필요") from None
    if not isinstance(run_directory, (str, os.PathLike)):
        raise SnapshotCollectionError("저장 디렉터리 경로 필요")
    if not all(hasattr(os, name) for name in ("O_DIRECTORY", "O_NOFOLLOW", "O_CLOEXEC")):
        raise SnapshotCollectionError("안전한 dir_fd/O_NOFOLLOW 저장 환경 필요")
    try:
        opened = os.open(run_directory, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW | os.O_CLOEXEC)
        try:
            try:
                os.stat(phase, dir_fd=opened, follow_symlinks=False)
            except FileNotFoundError:
                return
            raise SnapshotCollectionError("이미 존재하는 단계: 송신하지 않음")
        finally:
            os.close(opened)
    except SnapshotCollectionError:
        raise
    except (OSError, ValueError):
        raise SnapshotCollectionError("저장 경로 사전 검증 실패") from None


def _decode_chunks(wire: bytes) -> bytes:
    chunks, position = [], 0
    while True:
        end = wire.find(b"\r\n", position)
        if end < 0:
            raise SnapshotCollectionError("chunked 응답 길이 미완료")
        size_text = wire[position:end].split(b";", 1)[0]
        if not size_text or any(char not in b"0123456789abcdefABCDEF" for char in size_text):
            raise SnapshotCollectionError("chunked 응답 길이 부정")
        size = int(size_text, 16)
        position = end + 2
        if size == 0:
            if wire[position:] != b"\r\n":
                raise SnapshotCollectionError("chunked trailer/잔여 데이터는 지원하지 않음")
            return b"".join(chunks)
        if size > len(wire) - position or wire[position + size:position + size + 2] != b"\r\n":
            raise SnapshotCollectionError("chunked 응답 body 미완료")
        chunks.append(wire[position:position + size])
        position += size + 2


def _reject_token_fields(raw: bytes) -> None:
    try:
        pending = [json.loads(raw)]
    except (ValueError, UnicodeError, RecursionError):
        raise SnapshotCollectionError("스냅샷 JSON body 부정") from None
    aliases = {"x-api-key", "api-key", "apikey", "xapikey", "access-token", "refresh-token"}
    while pending:
        value = pending.pop()
        if isinstance(value, dict):
            for key, item in value.items():
                if key.strip().casefold().replace("_", "-") in aliases and item not in (None, "[REDACTED]"):
                    raise SnapshotCollectionError("스냅샷 body에 인증 필드값이 있음")
                pending.append(item)
        elif isinstance(value, list):
            pending.extend(value)


def _read_response(response: http.client.HTTPResponse, maximum: int, json_only: bool = True) -> bytes:
    if response.status != 200:
        raise SnapshotCollectionError(f"스냅샷 HTTP 상태 부정: {response.status}")
    content_type = response.getheader("Content-Type", "").split(";", 1)[0].strip().lower()
    if json_only and content_type != "application/json":
        raise SnapshotCollectionError("JSON 응답 Content-Type 필요")
    if response.getheader("Content-Encoding", "identity").strip().lower() != "identity":
        raise SnapshotCollectionError("압축 응답은 지원하지 않음")
    length_header = response.getheader("Content-Length")
    transfer = response.getheader("Transfer-Encoding")
    if transfer is not None and (transfer.strip().lower() != "chunked" or length_header is not None):
        raise SnapshotCollectionError("응답 전송 framing 부정")
    expected = None
    if length_header is not None:
        text = length_header.strip()
        if not text or not text.isascii() or not text.isdecimal():
            raise SnapshotCollectionError("응답 길이 부정")
        try:
            expected = int(text)
        except ValueError:
            raise SnapshotCollectionError("응답 길이 부정") from None
        if expected > maximum:
            raise SnapshotCollectionError("응답 크기 상한 초과")
    # HTTPResponse.read는 Content-Length/종료 chunk 뒤를 숨긴다. close 요청의 실제
    # EOF까지 bounded stream을 읽어 뒤따르는 바이트도 검증한다.
    if response.fp is None:
        raise SnapshotCollectionError("응답 body stream 부재")
    wire = response.fp.read(maximum + 1)
    if len(wire) > maximum:
        raise SnapshotCollectionError("응답 크기 상한 초과")
    if expected is not None and len(wire) != expected:
        raise SnapshotCollectionError("응답 body 길이 불일치")
    return _decode_chunks(wire) if transfer is not None else wire


def collect_snapshot(config: SnapshotEndpoint, run_directory: str | Path, phase: str,
                     context: SnapshotContext, *, opaque: bool = False) -> CollectedSnapshot:
    """독립 계측 GET 1회→원본/정규화 저장. 호출자가 등록한 읽기 전용 계측 path가 필요하다.

    timeout_seconds는 연결/읽기의 socket 비활성 제한이다. 스캔 전체 시간 예산이 아니다.
    received_at은 body 수신 완료 시각이며 엔진 요청 종료나 해결 주체를 증명하지 않는다.
    """
    host, port, host_header = _endpoint(config)
    _output_ready(run_directory, phase, context)
    requested = datetime.now(timezone.utc)
    try:
        # 직접 HTTPConnection을 사용해 HTTP_PROXY 등 환경 설정과 cookie jar를 배제한다.
        with closing(http.client.HTTPConnection(host, port, timeout=config.timeout_seconds)) as connection:
            connection.request("GET", config.path, headers={
                "Host": host_header,
                "Accept": "*/*" if opaque else "application/json",
                "Accept-Encoding": "identity",
                "Connection": "close",
            })
            with closing(connection.getresponse()) as response:
                raw = _read_response(response, config.max_bytes, json_only=not opaque)
    except SnapshotCollectionError:
        raise
    except (OSError, http.client.HTTPException, ValueError, OverflowError):
        raise SnapshotCollectionError("스냅샷 HTTP 수집 실패") from None
    received = datetime.now(timezone.utc)
    if received < requested:
        raise SnapshotCollectionError("수집 시계 역행")
    if not opaque:
        _reject_token_fields(raw)  # opaque는 원문을 저장하지 않으므로 검사할 필드도 남지 않는다
    try:
        artifacts = write_snapshot(run_directory, phase, raw, context, max_bytes=config.max_bytes, opaque=opaque)
    except SnapshotWriteError:
        raise SnapshotCollectionError("스냅샷 원본 검증/저장 실패") from None
    return CollectedSnapshot(artifacts, requested.isoformat(), received.isoformat())
