// 엔드포인트 하나의 요청·응답 예시 글. 원본 exFor(e) (host 는 전역 target 대신 인자로 받음).

import { EX, IDS, QS, REASON } from "../data/examples";
import type { Endpoint } from "../data/scope";

export interface ExView {
  /** 탐색에서 실제로 주고받음 → 출처 '예시 · 관찰' */
  readonly sent: boolean;
  /** 요청 예시 글 (줄바꿈 \n) */
  readonly req: string;
  /** 응답 예시 글. 보내지 않는 요청이면 null */
  readonly res: string | null;
  /** 응답이 없을 때의 안내 */
  readonly none: string | null;
}

function accept(p: string): string {
  if (p.indexOf("/api/") === 0 || p === "/health") return "application/json";
  return /\.(txt|xml)$/.test(p) ? "*/*" : "text/html";
}

/** 원본 `exFor(e)`. host 는 `curHost(target)`. */
export function exFor(e: Endpoint, host: string): ExView {
  const key = `${e.m} ${e.p}`;
  const x = EX[key] ?? {};
  const r = x.res ?? {};
  // {id} 는 첫 자리만 바꾼다 (원본 String.replace)
  const path = e.p.replace("{id}", IDS[e.p] ?? "1") + (QS[key] ?? "");
  const req = [`${e.m} ${path} HTTP/1.1`, `Host: ${host}`, `Accept: ${accept(e.p)}`];
  if (e.auth) req.push("Cookie: session=<계정 A 세션>");
  if (x.req) req.push("Content-Type: application/json", "", ...JSON.stringify(x.req, null, 2).split("\n"));
  const sent = (e.m === "GET" && e.k !== "off") || e.k === "login";
  let res: string | null = null;
  let none: string | null = null;
  if (r.blocked) none = "보내지 않는 요청이라 응답이 없습니다.";
  else {
    // 코드가 없으면 관찰한 응답 '200 / 401' 의 첫 숫자, 그것도 아니면 200
    const code = r.code ?? (Number.parseInt(e.st, 10) || 200);
    const L = [`HTTP/1.1 ${code} ${REASON[code] ?? ""}`];
    let body: string | null = null;
    let ct: string | null = null;
    if (r.json !== undefined) {
      ct = "application/json";
      body = JSON.stringify(r.json, null, 2);
    } else if (r.html) {
      ct = "text/html; charset=utf-8";
      body = r.html;
    } else if (r.text) {
      ct = r.ct ?? null;
      body = r.text.replace(/\{host\}/g, host);
    }
    if (ct && !r.none) L.push(`Content-Type: ${ct}`);
    for (const h of r.hdr ?? []) L.push(h);
    if (body && !r.none) L.push("", ...body.split("\n"));
    res = L.join("\n");
  }
  return { sent, req: req.join("\n"), res, none };
}
