# syntax=docker/dockerfile:1

# 제품 UI·Go 이미지. 저장소 루트에서 실행한다:
# docker build -f deploy/product/Dockerfile --platform linux/amd64 --build-arg VCS_REF=<소스 SHA> -t abc2lab:local .
# 공식 평가용 루트 Dockerfile은 기존 도구 계층과 실행 조건을 유지한다.
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
RUN CGO_ENABLED=0 GOOS=linux GOARCH=$TARGETARCH go build -trimpath -tags embedui \
      -ldflags="-X github.com/Autumn-27/artex/server.ProductRevision=${VCS_REF}" -o /out/first-controlled-diagnosis ./cmd/first-controlled-diagnosis \
    && CGO_ENABLED=0 GOOS=linux GOARCH=$TARGETARCH go build -trimpath -o /out/e2-bash-sandbox ./cmd/e2-bash-sandbox

# Bash 도구와 기존 브라우저 CLI를 실행할 수 있는 이미지.
FROM node:26-bookworm-slim@sha256:add5c17c14752c54d2f3d519ec46a3815684d41c33f198bcb09fb998f7dcbf68 AS product-runtime
RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates curl ripgrep git jq python3 wget unzip util-linux \
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
COPY --from=go-build /out/first-controlled-diagnosis /app/first-controlled-diagnosis
COPY --from=go-build /out/e2-bash-sandbox /app/e2-bash-sandbox
COPY --chmod=755 deploy/product/install-entrypoint.sh /app/product-install-entrypoint
COPY --chmod=755 deploy/product/preparation-entrypoint.sh /app/product-preparation-entrypoint
COPY start.sh /app/start.sh
COPY LICENSE /app/LICENSE
COPY --chown=1000:1000 skills/ /app/skills/
RUN chmod +x /app/artex /app/start.sh \
    && mkdir -p /app/data /app/keys /app/policy \
    && chown -R 1000:1000 /app/data /app/keys /app/policy /home/node
USER 1000:1000
EXPOSE 8787
HEALTHCHECK --interval=15s --timeout=3s --start-period=30s --retries=4 \
  CMD curl -fsS http://127.0.0.1:8787/api/product/health >/dev/null || exit 1
ENTRYPOINT ["/app/first-controlled-diagnosis"]
CMD ["product-front", "-listen", ":8787", "-api-origin", "http://127.0.0.1:28081"]

# 수용 전용 이미지에는 합성 서버만 넣는다. 제품 서비스는 이 target을 사용하지 않는다.
FROM go-build AS acceptance-build
RUN CGO_ENABLED=0 GOOS=linux GOARCH=$TARGETARCH go build -trimpath -o /out/product-fixture ./cmd/product-fixture
FROM debian:bookworm-slim AS acceptance-fixture
COPY --from=acceptance-build /out/product-fixture /app/product-fixture
USER 1000:1000
ENTRYPOINT ["/app/product-fixture"]

# 탐색 브라우저 샌드박스(docs/contracts/exploration-boundary.md §9-1 분리 배치). Chromium과 Playwright MCP만 넣고
# 엔진 바이너리·제어 비밀은 넣지 않는다. 같은 이미지를 두 컨테이너가 쓴다. explore-browser(기본 ENTRYPOINT)는 Chromium만
# 띄워 탐색 경계의 브라우저 입구로만 나가고, explore-mcp(ENTRYPOINT /app/explore-mcp-entrypoint)는 엔진 제어망에서 MCP HTTP를
# 열고 CDP 연결망으로 Chromium에 붙는다. MCP와 Chromium은 같은 network namespace에 있지 않다.
# 기본 빌드 대상은 마지막 product 단계라 이 단계는 `--target explore-browser`로만 만든다.
FROM node:26-bookworm-slim@sha256:add5c17c14752c54d2f3d519ec46a3815684d41c33f198bcb09fb998f7dcbf68 AS explore-browser
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates libnss3-tools \
    && rm -rf /var/lib/apt/lists/*
ENV PLAYWRIGHT_BROWSERS_PATH=/ms-playwright HOME=/home/node
# MCP는 자기 패키지에 묶인 Playwright 판(0.0.84는 1.65.0-alpha)의 브라우저를 찾는다. 전역 playwright 패키지로 깔면
# 판이 달라 실행할 때 브라우저를 못 찾으므로, MCP 자신의 설치 명령으로 그 판의 Chromium만 깐다.
RUN npm install -g @playwright/mcp@0.0.84 \
    && playwright-mcp install-browser --with-deps chromium \
    && chmod -R a+rX /ms-playwright \
    && rm -rf /var/lib/apt/lists/*
ARG VCS_REF=dev
LABEL org.opencontainers.image.source="https://github.com/ABC2LAB/ABCDEF2LAB" \
      org.opencontainers.image.revision="${VCS_REF}" \
      org.opencontainers.image.licenses="AGPL-3.0" \
      org.opencontainers.image.description="ABC2LAB exploration browser sandbox, derived from jiwoochris/artex-global"
COPY --chmod=755 deploy/product/explore-browser-entrypoint.sh /app/explore-browser-entrypoint
COPY --chmod=755 deploy/product/explore-mcp-entrypoint.sh /app/explore-mcp-entrypoint
COPY --chmod=644 deploy/product/explore-chromium.js /app/explore-chromium.js
# Chromium 컨테이너는 Playwright 없이 Chromium을 직접 띄운다. MCP 고정판 Playwright가 찾는 그 판의 실행 파일을 고정 경로로 둔다.
RUN chromium="$(node -e "process.stdout.write(require('/usr/local/lib/node_modules/@playwright/mcp/node_modules/playwright-core').chromium.executablePath())")" \
    && test -x "$chromium" && ln -s "$chromium" /app/chromium
USER 1000:1000
ENTRYPOINT ["/app/explore-browser-entrypoint"]

FROM product-runtime AS product
