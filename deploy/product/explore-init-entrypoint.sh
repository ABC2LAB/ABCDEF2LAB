#!/bin/sh
# 탐색 경계 보호 입력 준비(compose explore-init, 한 번 실행). 운영자가 준 보호 파일(root 0600)을 소비자별 볼륨으로 나눈다.
# 경계 볼륨(/i): 설정·CA 인증서·CA 개인키·경로 가림 키, 경계 uid 1001 소유 0600(디렉터리 0700).
# 공개 CA 볼륨(/c): CA 인증서만 0644. 브라우저 샌드박스가 신뢰 저장소에 넣는다. 개인키는 여기 두지 않는다.
# 제어 소켓 볼륨(/s): 경계(1001)가 소켓을 만들고 엔진(gid 1000)이 들어갈 수 있게 1001:1000 0750. 소켓 자체는 경계가
# 0600·엔진 uid 소유로 넘긴다.
set -eu
umask 077
in=/run/secrets
for name in explore_boundary_config explore_ca_cert explore_ca_key explore_path_key; do
  if [ ! -s "$in/$name" ]; then
    echo "탐색 경계 보호 입력이 없어 탐색 경계를 준비하지 않습니다: $name" >&2
    exit 1
  fi
done
chown 1001:1001 /i
chmod 0700 /i
install -o 1001 -g 1001 -m 0600 "$in/explore_boundary_config" /i/boundary.json
install -o 1001 -g 1001 -m 0600 "$in/explore_ca_cert" /i/ca.pem
install -o 1001 -g 1001 -m 0600 "$in/explore_ca_key" /i/ca.key
install -o 1001 -g 1001 -m 0600 "$in/explore_path_key" /i/path.key
# 대상이 사설 CA를 쓰는 온프레미스(또는 소유 수용 환경)는 그 CA(공개)를 경계 검증용으로 준다. 경계는 SPKI 고정값과 함께
# 이 CA로 대상 인증서 체인을 검증한다. 주지 않으면 시스템 루트다.
upstream="${EXPLORE_UPSTREAM_CA_SOURCE:-}"
if [ -n "$upstream" ]; then
  if [ ! -s "$upstream" ]; then
    echo "지정한 대상 CA 파일이 없어 탐색 경계를 준비하지 않습니다." >&2
    exit 1
  fi
  install -o 1001 -g 1001 -m 0600 "$upstream" /i/upstream.pem
fi
chown 0:0 /c
chmod 0755 /c
install -o 0 -g 0 -m 0644 "$in/explore_ca_cert" /c/boundary-ca.pem
chown 1001:1000 /s
chmod 0750 /s
