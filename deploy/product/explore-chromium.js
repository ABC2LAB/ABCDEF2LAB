'use strict';
// 탐색 브라우저 샌드박스의 Chromium 실행기(docs/contracts/exploration-boundary.md §9-1 분리 배치).
// 이 컨테이너에는 MCP 프로세스가 없다. 같은 namespace의 native 자식이 자기 MCP 제어 포트에 닿던 B16을 배치로 없앤다.
// MCP 컨테이너는 CDP 연결망에서 이 컨테이너의 CDP 중계에만 붙는다.
//
// Chromium 157은 --remote-debugging-address를 무시하고 CDP를 127.0.0.1에만 연다(실측). 그래서 CDP 연결망 주소 하나에만
// 리슨하는 고정 TCP 중계를 둔다. loopback의 CDP는 이 namespace 안에서만 닿고, 페이지의 loopback 요청은
// --proxy-bypass-list=<-loopback>으로 경계를 거치게 해 막는다.
// 옵션은 compose(또는 소유 fixture 하네스)가 정하고 모델은 바꾸지 못한다. TLS 오류 무시 옵션은 넣지 않는다.
const net = require('node:net');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { spawn } = require('node:child_process');

// 빌드 단계가 MCP 고정판 Playwright의 Chromium 경로로 만든 링크다(Dockerfile explore-browser).
const CHROMIUM = '/app/chromium';
const CDP_LOOPBACK = '127.0.0.1';
const STOP_GRACE_MS = 5000;

// @playwright/mcp@0.0.84에 묶인 Playwright(1.65.0-alpha-2026-10-30)의 chromiumSwitches와 headless 추가 옵션이다.
// 이전 배치에서 MCP가 Chromium을 직접 띄울 때 쓰던 값 그대로라 배경 통신·페이지 동작이 바뀌지 않는다.
const DISABLED_FEATURES = [
  'AvoidUnnecessaryBeforeUnloadCheckSync', 'DestroyProfileOnBrowserClose', 'LensOverlay', 'MediaRouter',
  'PaintHolding', 'ThirdPartyStoragePartitioning', 'AvoidCorsURLLoaderRestartOnRedirect', 'TcpPortRandomizationWin',
  'OptimizationHints', 'NetworkTimeServiceQuerying', 'AimEnabled', 'PreconnectToSearch', 'msForceBrowserSignIn',
  'msEdgeUpdateLaunchServicesPreferredVersion',
];
const ENABLED_FEATURES = ['CDPScreenshotNewSurface', 'SkipListAccountsWithoutGaiaCookies'];
const PLAYWRIGHT_SWITCHES = [
  '--disable-field-trial-config', '--disable-background-networking', '--disable-background-timer-throttling',
  '--disable-backgrounding-occluded-windows', '--disable-back-forward-cache', '--disable-breakpad',
  '--disable-component-extensions-with-background-pages', '--disable-component-update', '--no-default-browser-check',
  '--disable-default-apps', '--disable-dev-shm-usage', '--disable-edgeupdater', '--disable-extensions',
  '--disable-features=' + DISABLED_FEATURES.join(','), '--enable-features=' + ENABLED_FEATURES.join(','),
  '--allow-pre-commit-input', '--disable-hang-monitor', '--disable-ipc-flooding-protection', '--disable-popup-blocking',
  '--disable-prompt-on-repost', '--disable-renderer-backgrounding', '--disable-updater-scheduler',
  '--force-color-profile=srgb', '--metrics-recording-only', '--no-first-run', '--password-store=basic',
  '--use-mock-keychain', '--no-service-autorun', '--disable-search-engine-choice-screen',
  '--unsafely-disable-devtools-self-xss-warnings', '--enable-unsafe-swiftshader', '--edge-skip-compat-layer-relaunch',
  '--disable-stack-profiler', '--disable-infobars', '--disable-sync', '--do-not-de-elevate',
];
const HEADLESS_SWITCHES = [
  '--headless', '--hide-scrollbars', '--mute-audio',
  '--blink-settings=primaryHoverType=2,availableHoverTypes=2,primaryPointerType=4,availablePointerTypes=4',
];
// 이전 배치도 `--browser chromium`(chrome-for-testing 채널)이라 MCP가 Linux에서 샌드박스를 끈 채 띄웠다.
// cap_drop ALL·no-new-privileges·기본 seccomp에서는 Chromium 샌드박스가 동작하지 않으므로 같은 조건을 명시한다.
const SANDBOX_SWITCHES = ['--no-sandbox'];

function fail(message) {
  process.stderr.write('탐색 Chromium 실행기: ' + message + '\n');
  process.exit(2);
}

function parseArgs(argv) {
  const allowed = new Set(['--proxy-server', '--cdp-listen']);
  const values = new Map();
  for (let i = 0; i < argv.length; i += 2) {
    const name = argv[i];
    const value = argv[i + 1];
    if (!allowed.has(name) || values.has(name)) fail('허용 목록 밖이거나 중복된 옵션입니다.');
    if (value === undefined || value.startsWith('--')) fail('값이 없는 옵션입니다.');
    values.set(name, value);
  }
  for (const name of allowed) if (!values.has(name)) fail('필수 옵션이 없습니다.');
  return values;
}

function proxyServer(raw) {
  let url;
  try {
    url = new URL(raw);
  } catch (_) {
    fail('프록시 주소 형식이 아닙니다.');
  }
  // 경계의 브라우저 입구 하나만 받는다. 인증 정보·경로·질의가 붙은 값은 받지 않는다.
  if (url.protocol !== 'http:' || !url.hostname || !url.port || url.username || url.password
      || url.pathname !== '/' || url.search || url.hash) {
    fail('프록시 주소는 http://<호스트>:<포트> 형식이어야 합니다.');
  }
  return 'http://' + url.host;
}

function cdpListen(raw) {
  const at = raw.lastIndexOf(':');
  const host = raw.slice(0, at);
  const port = Number(raw.slice(at + 1));
  // CDP 연결망 주소 하나에만 연다. 모든 인터페이스·loopback에는 열지 않는다.
  if (at < 1 || net.isIPv4(host) !== true || host.startsWith('127.') || host === '0.0.0.0'
      || !Number.isInteger(port) || port < 1 || port > 65535 || String(port) !== raw.slice(at + 1)) {
    fail('CDP 리슨은 CDP 연결망의 IPv4 주소:포트여야 합니다.');
  }
  return { host, port };
}

function main() {
  const options = parseArgs(process.argv.slice(2));
  const proxy = proxyServer(options.get('--proxy-server'));
  const listen = cdpListen(options.get('--cdp-listen'));
  // 프로필은 컨테이너마다 새 tmpfs 디렉터리다. 디스크에 남기지 않는다.
  const profile = fs.mkdtempSync(path.join(os.tmpdir(), 'explore-chromium-'));
  const args = [
    ...PLAYWRIGHT_SWITCHES, ...HEADLESS_SWITCHES, ...SANDBOX_SWITCHES,
    '--proxy-server=' + proxy, '--proxy-bypass-list=<-loopback>',
    '--user-data-dir=' + profile, '--remote-debugging-port=' + listen.port, '--no-startup-window',
  ];
  // Chromium 자체 로그에는 페이지 URL(질의 값 포함)이 섞일 수 있어 컨테이너 로그로 내보내지 않는다.
  // 이전 배치에서도 Playwright가 이 출력을 받아 버렸다. 실행기 자신의 고정 문구만 stderr로 남긴다.
  const chromium = spawn(CHROMIUM, args, { stdio: ['ignore', 'ignore', 'ignore'] });
  const relay = net.createServer((client) => {
    const upstream = net.connect({ host: CDP_LOOPBACK, port: listen.port });
    // 한쪽이 끊기면 다른 쪽도 닫는다. 연결 하나의 실패는 그 연결만 닫는다.
    const close = () => { client.destroy(); upstream.destroy(); };
    client.on('error', close);
    upstream.on('error', close);
    client.on('close', close);
    upstream.on('close', close);
    client.pipe(upstream);
    upstream.pipe(client);
  });
  let stopping = false;
  const stop = () => {
    if (stopping) return;
    stopping = true;
    relay.close();
    chromium.kill('SIGTERM');
    setTimeout(() => chromium.kill('SIGKILL'), STOP_GRACE_MS).unref();
  };
  relay.on('error', () => {
    process.stderr.write('탐색 Chromium 실행기: CDP 중계 리슨에 실패했습니다.\n');
    process.exitCode = 1;
    stop();
  });
  chromium.on('error', () => {
    process.stderr.write('탐색 Chromium 실행기: Chromium을 시작하지 못했습니다.\n');
    process.exitCode = 1;
    relay.close();
  });
  chromium.on('exit', (code) => {
    relay.close();
    if (!stopping && process.exitCode === undefined) {
      process.stderr.write('탐색 Chromium 실행기: Chromium이 예기치 않게 종료했습니다.\n');
      process.exitCode = code === 0 ? 1 : (code || 1);
    }
  });
  // PID 1이라 신호 처리기를 직접 둔다. 컨테이너 중지는 Chromium 종료로 이어진다.
  process.on('SIGTERM', stop);
  process.on('SIGINT', stop);
  relay.listen({ host: listen.host, port: listen.port, exclusive: true });
}

main();
