#!/usr/bin/env bash
# 설치 입력은 운영자가 별도 보호 파일로 준비한다. 자동 비밀 생성·호스트 변경은 하지 않는다.
set -euo pipefail
cd "$(cd "$(dirname "$0")" && pwd)"
[[ $# -eq 0 ]] || { echo '사용법: 환경변수 준비 후 ./install.sh (deploy/product/README.md 참조)' >&2; exit 2; }
./scripts/product-compose.sh config --quiet
./scripts/product-compose.sh build
./scripts/product-compose.sh up -d --wait
printf '%s\n' "Workbench: http://127.0.0.1:${PRODUCT_PORT:-8787}/ · 로그인 이름 ABC2LAB" '로그: ./scripts/product-compose.sh logs -f preparation api workbench'
