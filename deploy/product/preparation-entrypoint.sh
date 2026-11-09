#!/bin/sh
set -eu
# 실제 helper의 namespace·읽기 전용 mount·seccomp·종료를 먼저 검사한다.
# 고정 true 명령만 실행하며 모델·대상 HTTP·실행 권위를 사용하지 않는다.
/app/first-controlled-diagnosis product-preflight
exec /app/first-controlled-diagnosis prepare-service -installation /p/config/installation.json
