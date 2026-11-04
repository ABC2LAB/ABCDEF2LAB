#!/usr/bin/env bash
# 패키지마다 go test -json을 돌리고 시험 단위 PASS/FAIL/SKIP 건수와 FAIL·SKIP 시험 이름을 요약한다.
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
} | tee -a "${GITHUB_STEP_SUMMARY:-/dev/null}"

exit "$status"
