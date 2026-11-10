# eval-e2 이미지: 엔진 이미지의 도구 계층을 그대로 쓰고 E② 서비스와 샌드박스 helper만 /app/e2에 얹는다.
# Bash가 이 컨테이너의 샌드박스에서 돌기 때문에 도구 계층이 엔진과 같아야 한다.
# /app은 scripts/eval/tool_tree.py 대조 범위 밖이라 엔진 이미지와의 대조가 그대로 성립한다.
# 빌드 컨텍스트는 바이너리만 든 임시 디렉터리다(레포 전체를 컨텍스트로 보내지 않음).
#   CGO_ENABLED=0 GOOS=linux go build -trimpath -o "$CTX/e2-eval-service" ./cmd/e2-eval-service
#   CGO_ENABLED=0 GOOS=linux go build -trimpath -o "$CTX/e2-bash-sandbox" ./cmd/e2-bash-sandbox
#   docker build -f deploy/eval/e2.Dockerfile --build-arg ENGINE_IMAGE="$EVAL_ENGINE_IMAGE" -t "$EVAL_E2_IMAGE" "$CTX"
# 실행 uid는 compose의 user로 러너 uid를 준다. 바이너리는 root 소유로 두어 그 uid가 바꾸지 못하게 한다.
ARG ENGINE_IMAGE
FROM ${ENGINE_IMAGE}
COPY e2-eval-service e2-bash-sandbox /app/e2/
ENTRYPOINT ["/app/e2/e2-eval-service"]
CMD []
