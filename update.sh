#!/usr/bin/env bash
# 운영자가 검토한 소스 SHA와 기존 보호 설치 입력을 재사용한다.
set -euo pipefail
cd "$(cd "$(dirname "$0")" && pwd)"
[[ $# -eq 0 ]] || { echo '사용법: 대상 소스를 체크아웃하고 환경변수 준비 후 ./update.sh' >&2; exit 2; }
./scripts/product-compose.sh config --quiet
./scripts/product-compose.sh build
./scripts/product-compose.sh up -d --wait
printf '%s\n' '제품 이미지를 다시 빌드하고 기존 설치를 검증·재사용했습니다.' '설치 형식 이관·승인 예산 재설정·볼륨 삭제는 수행하지 않습니다.'
