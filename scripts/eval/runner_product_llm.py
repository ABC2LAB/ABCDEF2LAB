"""탐색 분리 배치(개선 후 O) 회차의 제품 모델 호출 사후 검증 부품(탐색 분리 배치 결정안 D(b), 순서 4-11).

O는 게이트웨이를 거치지 않고 제품 자체 OpenRouter 경로로 모델을 부른다. 러너는 회차 뒤 세 가지를 본다.
1. 호출 기록이 빠짐없이 남았는지: 제품은 예약에 성공한 호출마다 승인 장부(`.openrouter-<승인 해시>.ledger`)에 예약 줄(`r1`)을
   남기고, 그 호출이 끝나면 호출 기록(`.calls`)에 한 줄을 남긴다(llmpolicy openrouter_transport.go: 예약 뒤 반환 경로 없이
   기록을 defer). 운영 경로의 예약은 transport 한 곳뿐이라 두 줄 수는 같아야 한다. 호출 기록 순번은 "기존 줄 수 + 1"이라
   기록 실패가 순번에 빈칸을 남기지 않는다. 그래서 순번이 아니라 장부와 대조한다. 다르면 사후 검증 근거가 불완전하다.
2. 제품이 실제로 보낸 샘플링 값과 덮어쓰기 횟수: 실행 기록 API(`GET /api/tasks/{id}/diagnosis-run`)의 실행·준비 모델 사용량.
   샘플링 값은 generation-verify 표식(`-sampling`)에 넣어 B 게이트웨이 적용값과 대조한다(runner_llm.judge_product_llm).
   덮어쓰기 횟수는 판정하지 않고 기록한다(4-4 사용자 결정: O는 기록, B는 게이트웨이 샘플링 거부 0건이 유효 조건).
3. generation 조회: 계측기 이미지의 generation-verify 역할(B 게이트웨이와 같은 조회 코드·셈)을 Docker로 돌린다
   (runner_docker.Docker.generation_verify). 키는 명령줄·파일에 싣지 않고 그 docker 호출의 환경으로만 넘긴다. 키 값은
   B 게이트웨이와 같은 규칙으로 얻는다(runner_docker.Docker.env_file_value: 같은 compose로 비밀 env 파일을 해석).
"""

from __future__ import annotations

import re

from runner_llm import SAMPLING_KEYS

# 제품 호출 기록 한 줄 상한(llmpolicy openRouterCallLineMax)과 같다. 파일 상한은 승인 요청 수 × 줄 상한이다.
CALL_LINE_MAX = 512
MAX_PRODUCT_FILE_BYTES = 16 << 20
_UINT = r"(?:0|[1-9][0-9]{0,18})"
_RESERVE = re.compile(rf"r1 {_UINT} {_UINT} {_UINT}\Z")
_SETTLE = re.compile(rf"s1 {_UINT} {_UINT} {_UINT} {_UINT} {_UINT} {_UINT}\Z")


class ProductLLMError(ValueError):
    """제품 모델 기록을 읽을 수 없음. 고정 code만 담는다(키·기록 원문은 담지 않는다)."""


def _lines(data: bytes, code: str) -> list[str]:
    """줄바꿈으로 끝나는 ASCII 줄 목록. 빈 파일은 빈 목록이다."""
    if not isinstance(data, bytes) or len(data) > MAX_PRODUCT_FILE_BYTES or (data and not data.endswith(b"\n")):
        raise ProductLLMError(code)
    try:
        text = data.decode("ascii")
    except UnicodeError:
        raise ProductLLMError(code) from None
    return text[:-1].split("\n") if text else []


def ledger_reservations(data: bytes) -> int:
    """승인 장부의 예약 줄(`r1 입력 출력 비용`) 수. 정산 줄(`s1` 여섯 값)은 세지 않는다. 다른 모양이면 거부한다."""
    count = 0
    for line in _lines(data, "product_ledger_invalid"):
        if _RESERVE.fullmatch(line):
            count += 1
        elif not _SETTLE.fullmatch(line):
            raise ProductLLMError("product_ledger_invalid")
    return count


def ledger_totals(data: bytes) -> dict:
    """승인 장부의 누적 합계(llmpolicy openRouterLedgerScan과 같은 셈). 예약 줄마다 요청 1·비용 +예약, 정산 줄은 그 예약 대신
    실제 비용을 센다(비용 -= 예약 − 실제). 정산 줄의 짝 대조는 제품이 이미 했으므로 여기서는 셈만 한다."""
    requests = cost = 0
    for line in _lines(data, "product_ledger_invalid"):
        fields = line.split(" ")
        if _RESERVE.fullmatch(line):
            requests += 1
            cost += int(fields[3])
        elif _SETTLE.fullmatch(line) and int(fields[6]) <= int(fields[3]):
            cost -= int(fields[3]) - int(fields[6])
        else:
            raise ProductLLMError("product_ledger_invalid")
    return {"requests": requests, "cost": cost}


def call_record_lines(data: bytes) -> int:
    """호출 기록 줄 수. 줄 내용의 엄격 검사는 generation-verify가 한다(칸·순번·어휘). 여기서는 줄 경계·상한만 본다."""
    lines = _lines(data, "product_calls_invalid")
    if any(not line or len(line) + 1 > CALL_LINE_MAX for line in lines):
        raise ProductLLMError("product_calls_invalid")
    return len(lines)


def call_records_reasons(ledger: bytes, calls: bytes) -> list[str]:
    """호출 기록 완전성의 무효 사유. 장부 예약 수와 호출 기록 줄 수가 다르면 사후 검증 근거가 불완전하다."""
    try:
        reserved, recorded = ledger_reservations(ledger), call_record_lines(calls)
    except ProductLLMError as exc:
        return [str(exc)]
    return [] if reserved == recorded else ["llm_call_records_incomplete"]


def _usage(value: object) -> dict:
    """모델 사용량 하나에서 쓰는 칸만 확인해 꺼낸다(llmpolicy.OpenRouterUsage)."""
    if not isinstance(value, dict) or type(value.get("requests")) is not int or value["requests"] < 0:
        raise ProductLLMError("product_model_usage_invalid")
    if "sampling_overwrites" not in value:  # 칸 이전 기록이면 null로 오지 않고 빠진다
        raise ProductLLMError("product_model_usage_invalid")
    overwrites = value["sampling_overwrites"]
    if overwrites is not None and (type(overwrites) is not int or overwrites < 0 or overwrites > value["requests"]):
        raise ProductLLMError("product_model_usage_invalid")
    sampling = value.get("sampling")
    if sampling is not None and (not isinstance(sampling, dict) or set(sampling) != SAMPLING_KEYS
                                 or any(type(v) not in (int, float) for v in sampling.values())):  # bool은 type이 bool이라 걸린다
        raise ProductLLMError("product_model_usage_invalid")
    return {"requests": value["requests"], "sampling_overwrites": overwrites, "sampling": sampling}


def product_model_usage(record: object) -> dict:
    """실행 기록 API 응답에서 실행·준비 모델 사용량을 꺼낸다.

    sampling: 제품이 보낸 샘플링 값(모든 사용량이 같은 값이어야 한다, 같은 승인 manifest라서). 보낸 호출이 없으면 None.
    sampling_overwrites: 실행 칸 하나와 준비 기록마다의 값(v1·v2 승인이면 None). 합치지 않는다. 준비 기록의 셈 범위가
    task당 하나라는 보장을 러너가 갖고 있지 않기 때문이다. overwrote: 하나라도 0보다 크면 참, unknown: null이 하나라도 있으면 참.
    """
    if not isinstance(record, dict) or record.get("version") != 1:
        raise ProductLLMError("product_run_record_invalid")
    execution = None if record.get("model") is None else _usage(record["model"])
    preparation_records = record.get("model_preparation", [])  # 빈 목록이면 칸이 빠진다(omitempty)
    if not isinstance(preparation_records, list) or not all(isinstance(item, dict) for item in preparation_records):
        raise ProductLLMError("product_run_record_invalid")
    preparation = [_usage(item.get("usage")) for item in preparation_records]
    usages = ([execution] if execution else []) + preparation
    samplings = {tuple(sorted(u["sampling"].items())) for u in usages if u["sampling"] is not None}
    if len(samplings) > 1:
        raise ProductLLMError("product_sampling_inconsistent")
    counts = [u["sampling_overwrites"] for u in usages]
    return {"sampling": dict(next(iter(samplings))) if samplings else None,
            "sampling_overwrites": {"execution": None if execution is None else execution["sampling_overwrites"],
                                    "preparation": [u["sampling_overwrites"] for u in preparation]},
            "requests": {"execution": None if execution is None else execution["requests"],
                         "preparation": [u["requests"] for u in preparation]},
            "overwrote": any(isinstance(c, int) and c > 0 for c in counts),
            "unknown": any(c is None for c in counts)}
