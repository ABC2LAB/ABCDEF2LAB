#!/bin/sh
# 탐색 브라우저 샌드박스의 MCP 컨테이너 시작(docs/contracts/exploration-boundary.md §5.1·§9-1 분리 배치).
# MCP는 Chromium을 띄우지 않고 --cdp-endpoint로 Chromium 컨테이너에 붙는다. 이 컨테이너는 엔진 제어망과 CDP 연결망에만
# 있고 브라우저 입구망에는 없다. 대상 요청은 Chromium이 경계를 거쳐 보내므로 여기서 CA·TLS 설정을 하지 않는다.
# MCP 옵션은 compose가 정하고 모델은 바꾸지 못한다.
set -eu
exec playwright-mcp "$@"
