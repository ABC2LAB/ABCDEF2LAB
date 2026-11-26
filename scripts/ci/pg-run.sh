#!/usr/bin/env bash
# pg-up.sh가 만든 시험 PG env 파일을 읽어 그 환경으로 명령을 실행한다. 사용법: pg-run.sh <env 파일> <명령...>
# env 파일은 셸로 읽지 않는다(`. 파일`·`source`). DSN 값의 `&`가 셸 문법으로 해석돼 DSN이 빠지고, PG가 필요한 시험이
# 조용히 SKIP되어 통과처럼 보인다(2026-11-27). 줄마다 이름=값으로 그대로 export한다.
# 실행 전에 파일의 DSN(이름이 _DSN으로 끝남)이 모두 들어갔고 접속되는지 확인하고, 하나라도 아니면 명령을 실행하지 않는다.
set -euo pipefail

env_file=${1:?사용법: pg-run.sh <env 파일> <명령...>}
shift
if [ "$#" -eq 0 ]; then
	echo "실행할 명령이 없음" >&2
	exit 2
fi

dsn_names=()
while IFS= read -r line || [ -n "$line" ]; do
	case "$line" in
	'' | '#'*) continue ;;
	*=*) ;;
	*)
		echo "env 파일 형식이 아님(이름=값): ${line%%=*}" >&2
		exit 2
		;;
	esac
	name=${line%%=*}
	if ! [[ "$name" =~ ^[A-Z_][A-Z0-9_]*$ ]]; then
		echo "env 이름 형식이 아님: $name" >&2
		exit 2
	fi
	export "$line"
	case "$name" in
	*_DSN) dsn_names+=("$name") ;;
	esac
done <"$env_file"

if [ "${#dsn_names[@]}" -eq 0 ]; then
	echo "env 파일에 DSN이 없음: $env_file" >&2
	exit 2
fi
ready=pg_isready
if [ -n "${CI_PG_BIN:-}" ]; then
	ready="$CI_PG_BIN/pg_isready"
fi
for name in "${dsn_names[@]}"; do
	value=${!name:-}
	if [ -z "$value" ]; then
		echo "DSN이 비어 있음: $name" >&2
		exit 2
	fi
	# 값은 출력하지 않는다(접속 정보).
	if ! "$ready" -q -t 5 -d "$value"; then
		echo "DSN에 접속하지 못함: $name" >&2
		exit 2
	fi
done
exec "$@"
