# eval-meter 이미지: 정적 바이너리 하나만 담는다. 외부 베이스 이미지를 들이지 않으려고 scratch를 쓴다.
# 빌드 컨텍스트는 바이너리만 든 임시 디렉터리다(레포 전체를 컨텍스트로 보내지 않음).
#   CGO_ENABLED=0 GOOS=linux go build -trimpath -o "$CTX/eval-meter" ./cmd/eval-meter
#   docker build -f deploy/eval/meter.Dockerfile -t "$EVAL_METER_IMAGE" "$CTX"
# 실행 uid는 compose의 user로 러너 uid를 준다(증거 디렉터리 0700 소유자와 같아야 함).
FROM scratch
COPY eval-meter /eval-meter
ENTRYPOINT ["/eval-meter"]
