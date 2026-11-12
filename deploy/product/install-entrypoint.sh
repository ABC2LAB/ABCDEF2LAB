#!/bin/sh
set -eu
# 설치 전용 일회성 서비스다. 런타임 서비스는 모두 비root이며 설치 비밀을 마운트하지 않는다.
install -d -m 0700 -o 1000 -g 1000 /p
for name in config data keys state runs ipc; do
 install -d -m 0700 -o 1000 -g 1000 "/p/$name"
done
install -d -m 0700 -o 1000 -g 1000 /tmp/product-private
install -m 0600 -o 1000 -g 1000 /run/secrets/product_install /tmp/product-private/install.json
install -m 0600 -o 1000 -g 1000 /run/secrets/postgres_password /tmp/product-private/postgres-password
# 탐색을 켠 설치만 경계 CA(공개 인증서)를 준다(target_policy.exploration.boundary_ca_file). 안 켜면 /dev/null이라 건너뛴다.
if [ -s /run/secrets/explore_ca_cert ]; then
 install -m 0600 -o 1000 -g 1000 /run/secrets/explore_ca_cert /tmp/product-private/explore-ca.pem
fi
exec setpriv --reuid=1000 --regid=1000 --clear-groups /app/first-controlled-diagnosis install-product -input /tmp/product-private/install.json -root /p
