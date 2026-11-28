#!/usr/bin/env python3
"""Make disposable Compose copies with an isolated owned Docker subnet.

Run from a dedicated worktree. Generated files remain beside the source
Compose files so relative seccomp and build paths retain their meaning.
"""

import argparse
from pathlib import Path


SOURCES = {
    "base": "compose.yaml",
    "acceptance": "compose.acceptance.yaml",
    "selected": "compose.selected-acceptance.yaml",
}
MODES = {
    "S01": "selected-model-stored-nosniff",
    "S02": "selected-model-nosniff",
    "S03": "selected-model-auth-nosniff",
    "S04": "selected-model-auth-nosniff",
    "S05": "selected-model-structured-nosniff",
    "S06": "selected-model-unsupported-write",
    "S07": "selected-model-nosniff",
    "S07B": "selected-model-nosniff",
    "S07P": "selected-model-nosniff",
    "S07R": "selected-model-structured-nosniff",
    "S08": "selected-model-overconfident-nosniff",
    "S09": "selected-model-nosniff",
    "S09C": "selected-model-structured-nosniff",
    "S09R": "selected-model-structured-nosniff",
    "S09M": "selected-model-nosniff",
}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("scenario", choices=MODES)
    parser.add_argument("--network-prefix", default="10.93", choices=("10.93",))
    args = parser.parse_args()
    directory = Path(__file__).resolve().parents[2] / "deploy" / "product"
    paths = []
    for part, source in SOURCES.items():
        output = directory / f".compose.pr950-{part}-local.yaml"
        output.write_text((directory / source).read_text(encoding="utf-8").replace("172.31.",
                          args.network_prefix + "."), encoding="utf-8")
        paths.append(output)
    scenario = directory / ".compose.pr950-scenario-local.yaml"
    services = ["services:"]
    if args.scenario in ("S03", "S04"):
        services += ["  target-front:", "    command: [-mode, selected-target-auth-nosniff, -hosts, synthetic.test=10.93.253.20]"]
    # 인증 후보 시나리오의 합성 모델은 지침의 등록 계정 줄로 대상 로그인 API에 직접 로그인한다(경로만 여기서 준다).
    login = ", -login-path, /login" if args.scenario in ("S03", "S04") else ""
    services += ["  synthetic-model:",
                 f"    command: [-mode, {MODES[args.scenario]}, -target, https://synthetic.test:9443{login}]"]
    scenario.write_text("\n".join(services) + "\n", encoding="utf-8")
    paths.append(scenario)
    print(" ".join(str(path) for path in paths))


if __name__ == "__main__":
    main()
