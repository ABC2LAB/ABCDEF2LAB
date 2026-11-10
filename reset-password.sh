#!/usr/bin/env bash
# 제품 PostgreSQL의 관리자 로그인 해시만 재설정한다. 런타임 역할은 사용하지 않는다.
set -euo pipefail
cd "$(cd "$(dirname "$0")" && pwd)"
if [[ $# -gt 0 ]]; then
  echo '사용법: 제품 Compose 환경변수 준비 후 ./reset-password.sh (비밀번호는 숨김 입력)' >&2
  [[ "$1" = --help || "$1" = -h ]] && exit 0
  exit 2
fi
./scripts/product-compose.sh config --quiet
read -rsp 'ABC2LAB 새 관리자 비밀번호(12자 이상): ' ABC2LAB_RESET_PASSWORD; echo >&2
read -rsp '확인을 위해 다시 입력: ' confirmation; echo >&2
[[ ${#ABC2LAB_RESET_PASSWORD} -ge 12 && "$ABC2LAB_RESET_PASSWORD" = "$confirmation" ]] || { echo '비밀번호 길이 또는 확인이 일치하지 않습니다.' >&2; exit 2; }
trap 'unset ABC2LAB_RESET_PASSWORD confirmation ABC2LAB_RESET_HASH' EXIT
ABC2LAB_RESET_HASH=$(printf '%s' "$ABC2LAB_RESET_PASSWORD" | ./scripts/product-compose.sh exec -T workbench /app/first-controlled-diagnosis product-password-hash)
unset ABC2LAB_RESET_PASSWORD confirmation
[[ "$ABC2LAB_RESET_HASH" =~ ^\$2[aby]\$[0-9]{2}\$[./A-Za-z0-9]{53}$ ]] || { echo '비밀번호 해시를 만들 수 없습니다.' >&2; exit 1; }
export ABC2LAB_RESET_HASH
./scripts/product-compose.sh exec -T -e ABC2LAB_RESET_HASH postgres sh -eu -c 'exec psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -v ON_ERROR_STOP=1 -q' <<'SQL'
\getenv newhash ABC2LAB_RESET_HASH
INSERT INTO settings(key, value)
VALUES ('auth.password_hash', :'newhash')
ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now();
SQL
printf '%s\n' 'ABC2LAB 관리자 비밀번호를 재설정했습니다. 기존 로그인 토큰은 무효화됩니다.'
