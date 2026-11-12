#!/bin/sh
# 탐색 브라우저 샌드박스의 Chromium 컨테이너 시작(docs/contracts/exploration-boundary.md §5·§9-1 분리 배치).
# 이 컨테이너에는 MCP가 없다. MCP는 별도 컨테이너(explore-mcp-entrypoint.sh)에서 CDP로 붙는다.
# 탐색 경계의 CA를 Chromium(NSS)에 신뢰시킨 뒤 고정 실행기로 Chromium을 띄운다. 대상 TLS 검증은 경계가 하므로
# 여기서 TLS 검증을 끄는 옵션은 쓰지 않는다. 실행기 옵션은 compose가 정하고 모델은 바꾸지 못한다.
set -eu
ca=/run/explore-ca/boundary-ca.pem
if [ ! -s "$ca" ]; then
  echo '탐색 경계 CA 파일이 없어 브라우저 샌드박스를 시작하지 않습니다.' >&2
  exit 1
fi
nssdb="$HOME/.pki/nssdb"
mkdir -p "$nssdb"
certutil -d "sql:$nssdb" -N --empty-password
certutil -d "sql:$nssdb" -A -t 'C,,' -n explore-boundary -i "$ca"
exec node /app/explore-chromium.js "$@"
