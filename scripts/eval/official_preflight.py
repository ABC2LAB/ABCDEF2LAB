"""공식 프로파일 → 사전검사 선언. 회차 조건을 손으로 옮기지 않고 커밋된 프로파일 값과 지문 그대로 만든다(평가 프로토콜 §5.1).

실행: python3 -I -B scripts/eval/official_preflight.py <공식 프로파일> --baseline-sha <40자> --ours-sha <40자> --out <새 파일>

사전검사(preflight.py)는 baseline·개선 후 선언이 서로 같은지만 본다. 둘 다 같은 오타가 있어도 통과하므로, 공식 회차의
선언은 이 도구로 프로파일에서만 만든다. 만든 선언은 사전검사를 통과해야 쓰고(통과 못 하면 파일을 만들지 않음), 덮어쓰지 않는다.
종료 코드: 0 작성, 2 프로파일·SHA·사전검사 오류 또는 파일이 이미 있음.
"""

from __future__ import annotations

import argparse
import json
import os
import re
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

from preflight import PreflightError, check_preflight  # noqa: E402
from profile_fingerprint import ProfileError, _read_profile, generate_fingerprints  # noqa: E402

SOURCE_SHA = re.compile(r"^[0-9a-f]{40}$")
# 프로파일 environment에서 사전검사 선언으로 그대로 옮기는 필드(이름 같음).
ENVIRONMENT_FIELDS = ("target_image", "network_mode", "llm", "temperature", "sampling", "seed", "worker_count",
                      "context_window_k", "time_budget_seconds", "request_budget", "run_storage", "run_drain")


def build_preflight(profile_path: str | Path, baseline_sha: str, ours_sha: str) -> dict:
    """프로파일을 검증·지문화하고 양쪽 선언을 만든다. 사전검사를 통과한 선언만 돌려준다."""
    for name, sha in (("baseline", baseline_sha), ("ours", ours_sha)):
        if not SOURCE_SHA.match(sha):
            raise ValueError(f"--{name}-sha: 40자리 소문자 16진 커밋 SHA가 아님")
    fingerprints = generate_fingerprints(_read_profile(profile_path))
    # 지문은 위에서 검증한 값으로 만들고, 선언 값은 JSON 숫자 그대로 쓰려고 표준 파서로 다시 읽는다.
    profile = json.loads(Path(profile_path).read_text(encoding="utf-8"))
    profile_env, scope = profile["environment"], profile["scope"]
    shared = {field: profile_env[field] for field in ENVIRONMENT_FIELDS}
    shared.update(target_origin=scope["target_origin"], fixture_fingerprint=fingerprints.fixture_fingerprint,
                  environment_fingerprint=fingerprints.environment_fingerprint,
                  scope_fingerprint=fingerprints.scope_fingerprint)
    payload = {"pins": {"baseline_source_sha": baseline_sha, "target_image": profile_env["target_image"]},
               "baseline": dict(shared, source_sha=baseline_sha), "ours": dict(shared, source_sha=ours_sha)}
    check_preflight(payload)
    return payload


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="공식 프로파일에서 사전검사 선언 만들기 (평가 프로토콜 §5.1)")
    parser.add_argument("profile", help="커밋된 공식 프로파일(예: docs/evaluation/profiles/official-35b.json)")
    parser.add_argument("--baseline-sha", required=True, help="baseline 엔진 소스 커밋(전체 40자)")
    parser.add_argument("--ours-sha", required=True, help="개선 후 엔진 동결 커밋(전체 40자)")
    parser.add_argument("--out", required=True, help="쓸 새 파일(있으면 거부)")
    args = parser.parse_args(argv)
    try:
        payload = build_preflight(args.profile, args.baseline_sha, args.ours_sha)
    except (ProfileError, PreflightError, ValueError) as error:
        print(f"사전검사 선언을 만들지 않음: {error}", file=sys.stderr)
        return 2
    try:
        fd = os.open(args.out, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW | os.O_CLOEXEC, 0o644)
    except FileExistsError:
        print(f"{args.out}: 이미 있음 — 덮어쓰지 않는다", file=sys.stderr)
        return 2
    with os.fdopen(fd, "w", encoding="utf-8") as out:
        json.dump(payload, out, ensure_ascii=False, indent=1)
        out.write("\n")
    print(f"사전검사 선언 → {args.out} (지문 environment {payload['ours']['environment_fingerprint'][:12]}…)")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
