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
# race 보고는 경합한 두 접근이 앞부분에 있어 끝부분만으로는 원인이 안 보인다. 첫 보고의 앞부분을 따로 붙인다.
race_lines=40

# 실패한 말단 시험(하위 시험이 실패한 부모는 제외)과, 시험 단위 실패 없이 실패한 패키지(빌드 오류 등)의
# 출력 마지막 줄들. test2json의 output 이벤트는 그 출력을 낸 시험 이름으로, 컴파일 오류는 build-output으로 묶인다.
failure_output() {
	python3 -I - "$out" "$failure_lines" "$failure_tests" "$failure_width" "$race_lines" <<'PY'
import json
import sys
from collections import defaultdict, deque

path, per_test, max_tests, width, race_max = sys.argv[1], *map(int, sys.argv[2:6])
outputs = defaultdict(lambda: deque(maxlen=per_test))
races = defaultdict(list)  # 시험마다 첫 race 보고의 앞부분
builds = defaultdict(lambda: deque(maxlen=per_test))
failed_tests, failed_packages, failed_builds = [], [], {}


def clean(text):
    # 출력 안의 코드 펜스가 요약의 코드 블록을 끝내지 않게 바꾼다.
    return text.rstrip("\n")[:width].replace("```", "'''")


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
            text = clean(event.get("Output", ""))
            outputs[key].append(text)
            race = races[key]
            if (race and len(race) < race_max) or (not race and "WARNING: DATA RACE" in text):
                race.append(text)
        elif action == "fail" and key[1]:
            failed_tests.append(key)
        elif action == "fail":
            failed_packages.append(key)
            if event.get("FailedBuild"):
                failed_builds[key[0]] = event["FailedBuild"]
names = {key for key in failed_tests}
leaves = [key for key in failed_tests
          if not any(other[0] == key[0] and other[1].startswith(key[1] + "/") for other in names)]
with_tests = {package for package, _ in failed_tests}
targets = leaves + [key for key in failed_packages if key[0] not in with_tests]
for package, test in targets[:max_tests]:
    print(f"=== {package} {test or '(패키지 출력)'}")
    lines = list(outputs[(package, test)])
    if not test and package in failed_builds:
        lines = list(builds[failed_builds[package]]) + lines
    race = races.get((package, test), [])
    if race and race[0] not in lines:
        print("\n".join(race))
        print("… (race 보고 중간 생략)")
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
