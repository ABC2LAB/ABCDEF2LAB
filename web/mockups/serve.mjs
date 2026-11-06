// 작업대 시안(HTML 한 파일)을 웹 앱(3000)과 다른 포트로 띄우는 작은 정적 서버. 의존성 없음.
// 사용: npm run mockup -- -H 0.0.0.0        (기본 포트 3100, -p 로 바꿈)

import { readFile } from "node:fs/promises";
import { createServer } from "node:http";
import { networkInterfaces } from "node:os";
import process from "node:process";

const args = process.argv.slice(2);
function opt(short, long, fallback) {
  const i = args.findIndex((a) => a === short || a === long);
  return i >= 0 && args[i + 1] ? args[i + 1] : fallback;
}
const host = opt("-H", "--hostname", "localhost");
const port = Number(opt("-p", "--port", process.env.MOCKUP_PORT ?? "3100"));

// 보낼 수 있는 파일을 목록으로 고정한다. 주소를 조작해도(../ 등) 이 폴더의 다른 파일이나 상위 폴더는 읽지 못한다.
const PAGES = new Map([
  ["/", "abc2lab-workbench.html"],
  ["/index.html", "abc2lab-workbench.html"],
  ["/abc2lab-workbench.html", "abc2lab-workbench.html"],
]);

async function handle(req, res) {
  if (req.method !== "GET" && req.method !== "HEAD") {
    res.writeHead(405, { Allow: "GET, HEAD" }).end();
    return;
  }
  const path = new URL(req.url ?? "/", "http://localhost").pathname;
  const file = PAGES.get(path);
  if (!file) {
    res.writeHead(404, { "Content-Type": "text/plain; charset=utf-8" }).end("Not found");
    return;
  }
  try {
    // 요청마다 다시 읽어서, 파일을 고치면 새로고침만으로 반영된다
    const body = await readFile(new URL(file, import.meta.url));
    res.writeHead(200, {
      "Content-Type": "text/html; charset=utf-8",
      "Cache-Control": "no-store",
      "X-Content-Type-Options": "nosniff",
    });
    res.end(req.method === "HEAD" ? undefined : body);
  } catch {
    res.writeHead(500, { "Content-Type": "text/plain; charset=utf-8" }).end("Read error");
  }
}

const server = createServer((req, res) => {
  handle(req, res).catch(() => res.destroy());
});
server.on("error", (e) => {
  console.error(
    e.code === "EADDRINUSE" ? `포트 ${port}을(를) 이미 쓰고 있습니다. -p 로 다른 번호를 주세요.` : e.message,
  );
  process.exit(1);
});
server.listen(port, host, () => {
  console.log(`작업대 시안: http://localhost:${port}`);
  if (host === "0.0.0.0" || host === "::") {
    for (const list of Object.values(networkInterfaces())) {
      for (const a of list ?? []) {
        if (a.family === "IPv4" && !a.internal) console.log(`같은 네트워크: http://${a.address}:${port}`);
      }
    }
  }
});
