#!/usr/bin/env bash
# 보호된 설치 입력을 사용하는 제품 Compose 공통 진입점.
set -euo pipefail
cd "$(cd "$(dirname "$0")/.." && pwd)"
if [[ ! "${PRODUCT_SOURCE_SHA:-}" =~ ^[0-9a-f]{40}$ ]]; then
  echo 'PRODUCT_SOURCE_SHA에 빌드할 소스의 전체 SHA를 지정하세요.' >&2; exit 2
fi
for name in PRODUCT_INSTALL_FILE PRODUCT_POSTGRES_PASSWORD_FILE; do
  file="${!name:-}"
  [[ "$file" = /* && -f "$file" ]] || { echo "$name 에 저장소 밖 보호 파일의 절대 경로를 지정하세요. deploy/product/README.md 참조." >&2; exit 2; }
done
command -v docker >/dev/null || { echo 'Docker Engine 26+와 Compose 2.35+가 필요합니다.' >&2; exit 2; }
exec docker compose -f deploy/product/compose.yaml "$@"
