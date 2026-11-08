#!/usr/bin/env bash
# 패키지마다 go test -json을 돌리고 시험 단위 PASS/FAIL/SKIP 건수와 FAIL·SKIP 시험 이름을 요약한다.
# 실패한 시험은 출력 끝부분도 요약에 붙인다(간헐 실패를 다시 돌리지 않고 원인을 보려고).
# 필요한 DB 시험의 SKIP은 통과가 아니므로(CONTRIBUTING.md) SKIP 목록을 숨기지 않는다.
# CI_PG_*가 있으면(pg-up.sh) 패키지마다 시험 DB를 새로 만든다. 앞 패키지가 남긴 행에 따라 결과가
# 갈리지 않게 하려는 것이고, 한 패키지 안에서 시험끼리 남기는 부작용은 그대로 드러난다.
# 하나라도 실패하면 1로 끝난다.
set -uo pipefail

out=${1:?사용법: go-test.sh <json 출력 파일> <패키지 패턴> [go test 인자...]}
pattern=${2:?사용법: go-test.sh <json 출력 파일> <패키지 패턴> [go test 인자...]}
shift 2

reset_databases() {
	local db
	for db in $CI_PG_DATABASES; do
		"$CI_PG_BIN/dropdb" --if-exists --force --host="$CI_PG_SOCKET" --username="$CI_PG_USER" "$db" &&
			"$CI_PG_BIN/createdb" --host="$CI_PG_SOCKET" --username="$CI_PG_USER" "$db" || return 1
	done
}

: >"$out"
status=0
mapfile -t packages < <(go list "$pattern")
if [ "${#packages[@]}" -eq 0 ]; then
	echo "패키지 없음: $pattern" >&2
	exit 1
fi
for package in "${packages[@]}"; do
	if [ -n "${CI_PG_SOCKET:-}" ] && ! reset_databases; then
		echo "시험 DB를 새로 만들지 못함: $package" >&2
		exit 1
	fi
	go test -json "$@" "$package" >>"$out" || status=1
done

# test2json 이벤트는 Time·Action·Package·Test 순서로 직렬화된다.
events() {
	grep -E "\"Action\":\"$1\",\"Package\":\"[^\"]+\",\"Test\":\"" "$out" |
		sed -E 's/.*"Package":"([^"]+)","Test":"([^"]+)".*/\1 \2/' || true
}
count() {
	events "$1" | grep -c . || true
}
summary_limit=200
# 실패 시험마다 붙이는 출력 줄 수와 시험 수, 줄 길이 상한. 전체 출력은 첫 인자 파일에만 있다(CI는 업로드하지 않음).
failure_lines=40
failure_tests=20
failure_width=300
# race 보고(경합한 두 접근)와 panic·시간 초과(원인 줄)는 앞부분에 원인이 있고 뒤는 goroutine 덤프라
# 끝부분만으로는 안 보인다. 첫 보고의 앞부분(바로 앞 몇 줄 포함)을 따로 붙인다.
head_lines=40
head_context=5

# 실패한 말단 시험(하위 시험이 실패한 부모는 제외), 실패한 패키지에서 끝나지 않은 말단 시험(시간 초과 등),
# 그 밖에 시험 단위 근거 없이 실패한 패키지(빌드 오류 등)의 출력 마지막 줄들. test2json의 output 이벤트는
# 그 출력을 낸 시험 이름으로, 컴파일 오류는 build-output으로 묶인다.
failure_output() {
	python3 -I - "$out" "$failure_lines" "$failure_tests" "$failure_width" "$head_lines" "$head_context" <<'PY'
import json
import sys
from collections import defaultdict, deque

path, per_test, max_tests, width, head_max, context = sys.argv[1], *map(int, sys.argv[2:7])
outputs = defaultdict(lambda: deque(maxlen=per_test))  # (출력 순번, 줄)
counts = defaultdict(int)
heads = defaultdict(list)  # 시험마다 첫 race·panic 보고의 앞부분 (출력 순번, 줄)
recent = defaultdict(lambda: deque(maxlen=context))
builds = defaultdict(lambda: deque(maxlen=per_test))
started, finished = [], set()
failed_tests, failed_packages, failed_builds = [], [], {}


def clean(text):
    # 출력 안의 코드 펜스가 요약의 코드 블록을 끝내지 않게 바꾼다.
    return text.rstrip("\n")[:width].replace("```", "'''")


def leaves(keys):
    # 하위 시험이 목록에 있는 부모는 뺀다(원인은 하위 시험 출력에 있다).
    names = set(keys)
    return [key for key in keys
            if not any(other[0] == key[0] and other[1].startswith(key[1] + "/") for other in names)]


with open(path, encoding="utf-8", errors="replace") as stream:
    for line in stream:
        try:
            event = json.loads(line)
        except ValueError:
            continue
        action, key = event.get("Action"), (event.get("Package", ""), event.get("Test") or "")
        if action == "build-output":
            builds[event.get("ImportPath", "")].append(clean(event.get("Output", "")))
        elif action == "output":
            index, text = counts[key], clean(event.get("Output", ""))
            counts[key] += 1
            outputs[key].append((index, text))
            head = heads[key]
            if head and len(head) < head_max:
                head.append((index, text))
            elif not head and ("WARNING: DATA RACE" in text or text.startswith("panic: ")):
                head.extend(recent[key])
                head.append((index, text))
            recent[key].append((index, text))
        elif action == "run" and key[1]:
            # -count=N 반복에서는 같은 시험이 다시 돈다. 앞 회차의 완료가 이번 미완료 실행을 가리지 않게 지운다.
            started.append(key)
            finished.discard(key)
        elif action in ("pass", "skip") and key[1]:
            finished.add(key)
        elif action == "fail" and key[1]:
            finished.add(key)
            failed_tests.append(key)
        elif action == "fail":
            failed_packages.append(key)
            if event.get("FailedBuild"):
                failed_builds[key[0]] = event["FailedBuild"]
failed_package_names = {package for package, _ in failed_packages}
# 시간 초과·프로세스 종료로 실패한 패키지는 시험별 fail 이벤트 없이 끝나고, 원인 출력은 끝나지 않은 시험 이름으로 남는다.
unfinished = leaves([key for key in dict.fromkeys(started)
                     if key not in finished and key[0] in failed_package_names])
covered = {package for package, _ in failed_tests} | {package for package, _ in unfinished}
# 반복 실행에서 같은 시험이 실패와 미완료로 둘 다 잡혀도 한 번만 보인다.
targets = list(dict.fromkeys(leaves(list(dict.fromkeys(failed_tests))) + unfinished
                             + [key for key in failed_packages if key[0] not in covered]))
unfinished = set(unfinished)
for key in targets[:max_tests]:
    package, test = key
    print(f"=== {package} {test or '(패키지 출력)'}{' (완료 이벤트 없음)' if key in unfinished else ''}")
    tail = list(outputs[key])
    tail_start = tail[0][0] if tail else counts[key]
    # 꼬리에 들어가지 않은 앞부분만 출력 순번으로 골라 붙인다(같은 경고문이라도 다른 보고일 수 있음).
    head = [entry for entry in heads.get(key, []) if entry[0] < tail_start]
    lines = [text for _, text in tail]
    if not test and package in failed_builds:
        lines = list(builds[failed_builds[package]]) + lines
    if head:
        print("\n".join(text for _, text in head))
        if head[-1][0] + 1 < tail_start:
            print("… (중간 생략)")
    print("\n".join(lines[-per_test:]) or "(출력 없음)")
if len(targets) > max_tests:
    print(f"… 실패 {len(targets) - max_tests}개는 생략")
PY
}

{
	echo "### Go 시험 요약"
	echo
	echo "| 패키지 | PASS | FAIL | SKIP |"
	echo "|---|---|---|---|"
	echo "| ${#packages[@]} | $(count pass) | $(count fail) | $(count skip) |"
	echo
	failed_packages=$(grep -E '"Action":"fail","Package":"[^"]+","Elapsed"' "$out" |
		sed -E 's/.*"Package":"([^"]+)".*/\1/' | sort -u || true)
	if [ -n "$failed_packages" ]; then
		echo "**실패한 패키지**"
		echo '```'
		echo "$failed_packages"
		echo '```'
	fi
	for action in fail skip; do
		listed=$(events "$action" | sort -u)
		if [ -n "$listed" ]; then
			echo "**${action^^} 시험** (최대 ${summary_limit}줄)"
			echo '```'
			echo "$listed" | head -n "$summary_limit"
			echo '```'
		fi
	done
	if [ -n "$failed_packages" ]; then
		echo "**실패 출력** (시험마다 마지막 ${failure_lines}줄, 최대 ${failure_tests}개)"
		echo '```'
		failure_output
		echo '```'
	fi
} | tee -a "${GITHUB_STEP_SUMMARY:-/dev/null}"

exit "$status"
