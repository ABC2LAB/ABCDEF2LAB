#!/usr/bin/env bash
# 1회용 PostgreSQL 16 클러스터를 비어 있는 디렉터리에 만들고, 시험 DSN 변수마다 다른 DB를 만들어
# `이름=값` 줄로 출력한다(CI는 $GITHUB_ENV에 붙인다). TCP는 열지 않고 unix 소켓만 쓴다.
# 시험별 DB를 나누는 이유: budgetlocal 등은 일반 DB와 전용 DB가 같으면 돌지 않는다.
# CI_PG_* 값은 go-test.sh가 패키지마다 DB를 새로 만들 때 쓴다.
set -euo pipefail

root=${1:?사용법: pg-up.sh <비어 있는 작업 디렉터리>}
bin=${PG_BIN:-/usr/lib/postgresql/16/bin}
if [ ! -x "$bin/initdb" ]; then
	echo "PostgreSQL 16 바이너리 없음: $bin" >&2
	exit 1
fi
mkdir -p "$root"
if [ -n "$(ls -A "$root")" ]; then
	echo "작업 디렉터리가 비어 있지 않음: $root" >&2
	exit 1
fi
# unix 소켓 경로(sun_path)는 107바이트까지라 짧은 이름을 쓰고, 넘으면 시작 전에 멈춘다.
socket="$root/s"
socket_file="$socket/.s.PGSQL.5432"
if [ "${#socket_file}" -gt 107 ]; then
	echo "소켓 경로가 너무 김(${#socket_file}바이트 > 107): 더 짧은 작업 디렉터리를 쓰세요" >&2
	exit 1
fi
mkdir -m 0700 "$socket"
user=ci

"$bin/initdb" --pgdata="$root/data" --username="$user" --auth=trust --no-sync >/dev/null
"$bin/pg_ctl" --pgdata="$root/data" --log="$root/postgres.log" --wait \
	--options="-c listen_addresses='' -c unix_socket_directories='$socket' -c fsync=off" start >/dev/null

declare -A databases=(
	[ARTEX_PG_DSN]=artex
	[ARTEX_INTERCEPT_TEST_DSN]=artex_intercept
	[ARTEX_TASK_MODE_TEST_DSN]=artex_task_mode
	[ARTEX_E3_TEST_DSN]=artex_e3
	[ARTEX_AUDIT_TEST_DSN]=artex_audit
	[ENGINE_METRICS_TEST_DSN]=artex_engine_metrics
)
for name in $(printf '%s\n' "${!databases[@]}" | sort); do
	"$bin/createdb" --host="$socket" --username="$user" "${databases[$name]}"
	echo "$name=postgres:///${databases[$name]}?host=$socket&user=$user&sslmode=disable"
done
# cmd/engine-metrics의 PG 시험은 role·schema를 만들므로, 이 클러스터가 이 실행이 만든 1회용 인스턴스라는
# 표식을 요구한다(형식만 검사). 작업 디렉터리 경로의 해시로 채운다.
echo "ENGINE_METRICS_OWNED_TEST_PG=codex-owned-pg-v1:$(printf '%s' "$root" | sha256sum | cut -c1-64)"
echo "CI_PG_BIN=$bin"
echo "CI_PG_SOCKET=$socket"
echo "CI_PG_USER=$user"
echo "CI_PG_DATABASES=$(printf '%s\n' "${databases[@]}" | sort | tr '\n' ' ')"
