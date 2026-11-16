"""원본(B) 작업 본문의 목표 문장(탐색 분리 배치 결정안 §6, 2026-11-16 사용자 결정).

B 본문의 목표는 제품(O) 목표 문장의 공통 부분과 바이트까지 같다. 제품 쪽 원본은 Go
`urlDiagnosisCommonGoal`(cmd/first-controlled-diagnosis/url_preparation_runtime.go)이고, 예시 입력의 출력은
cmd/first-controlled-diagnosis/testdata/goal-common-example.txt에 고정돼 있다(Go 시험
`TestURLDiagnosisCommonGoalIsSharedAndPinned`). 러너 시험이 같은 입력의 이 렌더 결과 + 끝 줄바꿈 하나를 같은 파일과
비교하므로, 제품 문장이 바뀌면 testdata가 바뀌고 여기 시험이 실패한다. 회차 짝 대조는 공통 부분 SHA로 한 번 더 한다.

계정 라벨 문장은 "호스트가 시험 계정으로 로그인해 세션을 준다"고 말하므로, B에도 같은 세션을 주는 조건에서만 넣는다.
O에만 계정이 결속된 회차는 공통 SHA가 달라 짝으로 묶이지 않는다(의도한 거부).
"""

from __future__ import annotations

import hashlib

_PATHS_HEAD = " Approved paths on this origin, space-separated: "
_PATHS_TAIL = " . Each listed URL covers that path and everything under it, except a URL marked (exact), which covers only that path without a query string. The given target URL is one starting point among these, not the whole target: explore and test every approved path."
_EXACT = " (exact)"
_OBJECTIVE = (" Objective: find vulnerabilities and back each one with actual request evidence. Test authorization first: IDOR (one account reading or using another account's objects by changing an object ID) and broken access control (reaching admin or other-role data and functions without that role). Then test other web vulnerability types the approved methods can reach, such as injection, sensitive data exposure and security misconfiguration. Endpoint discovery and mapping are a means, not the objective: the task is done only when the discovered attack surface has been tested for these vulnerability types, not when it has been mapped."
              " Count authorization as tested only when the requests can show it: IDOR is tested when the same object on an endpoint that requires authentication is requested with each test account's session and the responses are compared; broken access control is tested when a function meant for another role is requested with a session that lacks that role and with no session. Reading data that any unauthenticated client can read does not by itself test IDOR or access control.")
_LABELS_HEAD = " The host logs in owned disposable test accounts (labels: "
_LABELS_TAIL = ") before diagnosis and gives Workers their sessions, so plan authorization intents that compare what each account can access."
_REPORT = " Report each potential vulnerability as an unconfirmed review candidate with its evidence; confirmation is decided separately from trusted evidence."


def common_goal(origin: str, paths: list[str], account_labels: list[str]) -> str:
    """제품 Go 렌더와 같은 순서·같은 바이트: 승인 경로 문장 → 목적 → 계정 라벨 문장 → 보고 문장.

    경로 표현은 제품 승인 입력 그대로다: `/x`는 그 경로와 아래 전체, `=/x`는 그 경로만(`(exact)` 표시).
    """
    text = ""
    if paths and origin:
        urls = [origin + p[1:] + _EXACT if p.startswith("=") else origin + p for p in paths]
        text += _PATHS_HEAD + " ".join(urls) + _PATHS_TAIL
    objective = _OBJECTIVE
    if account_labels:
        objective += _LABELS_HEAD + ", ".join(account_labels) + _LABELS_TAIL
    return text + objective + _REPORT


def common_goal_sha256(text: str) -> str:
    """제품 실행 기록 `goal.common_sha256`과 같은 계산(공통 부분 바이트 그대로, 앞 공백 포함)."""
    return hashlib.sha256(text.encode("utf-8")).hexdigest()
