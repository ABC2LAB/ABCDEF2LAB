# syntax=docker/dockerfile:1

# 저장소 루트에서: docker build --platform linux/amd64 --build-arg VCS_REF=<소스 SHA> -t abc2lab:local .
# 정적 UI와 Go 바이너리는 같은 소스 트리에서 빌드한다.
FROM --platform=$BUILDPLATFORM node:26-bookworm-slim@sha256:add5c17c14752c54d2f3d519ec46a3815684d41c33f198bcb09fb998f7dcbf68 AS web-build
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN HUSKY=0 npm ci --no-audit --no-fund
COPY web/ ./
ENV NEXT_TELEMETRY_DISABLED=1 NEXT_PUBLIC_MOCK=0
RUN rm -rf out .next && npm run build:static -- --webpack && test -s out/index.html

FROM --platform=$BUILDPLATFORM golang:1.26.3-bookworm@sha256:386d475a660466863d9f8c766fec64d7fdad3edac2c6a05020c09534d71edb4b AS go-build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . ./
# checkout에 남은 정적 파일이 이미지에 섞이지 않게 빌더에서 지운다.
RUN rm -rf server/webui/dist && mkdir -p server/webui/dist
COPY --from=web-build /src/web/out/ /src/server/webui/dist/
ARG TARGETARCH
ARG VCS_REF=dev
RUN CGO_ENABLED=0 GOOS=linux GOARCH=$TARGETARCH go build -trimpath -tags embedui \
      -ldflags="-X main.version=${VCS_REF}" -o /out/artex ./cmd/artex

# Bash 도구와 기존 브라우저 CLI를 실행할 수 있는 이미지.
FROM node:26-bookworm-slim@sha256:add5c17c14752c54d2f3d519ec46a3815684d41c33f198bcb09fb998f7dcbf68
RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates curl ripgrep git jq python3 wget unzip \
      dnsutils iputils-ping netcat-openbsd inetutils-telnet whois nmap \
    && rm -rf /var/lib/apt/lists/*
ENV PLAYWRIGHT_BROWSERS_PATH=/ms-playwright HOME=/home/node
RUN npm install -g @playwright/mcp@0.0.84 @playwright/cli@0.1.23 playwright@1.65.0 \
    && playwright-cli --help >/dev/null \
    && playwright install --with-deps chromium \
    && chmod -R a+rX /ms-playwright \
    && rm -rf /var/lib/apt/lists/*
ARG VCS_REF=dev
LABEL org.opencontainers.image.source="https://github.com/ABC2LAB/ABCDEF2LAB" \
      org.opencontainers.image.revision="${VCS_REF}" \
      org.opencontainers.image.licenses="AGPL-3.0" \
      org.opencontainers.image.description="ABC2LAB, derived from jiwoochris/artex-global"
WORKDIR /app
COPY --from=go-build /out/artex /app/artex
COPY start.sh /app/start.sh
COPY LICENSE /app/LICENSE
COPY --chown=1000:1000 skills/ /app/skills/
RUN chmod +x /app/artex /app/start.sh \
    && mkdir -p /app/data /app/keys /app/policy \
    && chown -R 1000:1000 /app/data /app/keys /app/policy /home/node
USER 1000:1000
VOLUME ["/app/data", "/app/keys"]
EXPOSE 8787
HEALTHCHECK --interval=15s --timeout=3s --start-period=30s --retries=4 \
  CMD curl -fsS http://127.0.0.1:8787/api/health >/dev/null || exit 1
ENTRYPOINT ["/app/start.sh"]
CMD ["-addr", ":8787", "-proxy", "127.0.0.1:8788", "-key-dir", "/app/keys"]
