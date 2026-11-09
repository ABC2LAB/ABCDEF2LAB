// 임시 비교 (곧 지움): 원본 시안 DOM 과 React DOM 을 같은 상태에서 정규화해 파일로 쓴다.
import "@testing-library/jest-dom/vitest";
import { act, cleanup, fireEvent, render } from "@testing-library/react";
import { readFileSync, writeFileSync } from "node:fs";
import { JSDOM } from "jsdom";
import { it } from "vitest";

import { createInitialState, useWb } from "../store";
import { Live } from "../views/Live";
import { Report } from "../views/Report";

const OUT = "/tmp/claude-0/-home-claude/d5499db6-ad63-53ec-bf56-a81a1b942de2/scratchpad/cmp";
const SRC = "/tmp/claude-0/-home-claude/d5499db6-ad63-53ec-bf56-a81a1b942de2/scratchpad/wbr/src.html";

const KEEP = /^(id|role|aria-.*|tabindex|hidden|href|type|title|scope|viewbox|x|y|width|height|rx|d|text-anchor|dx|marker-end|refx|refy|markerwidth|markerheight|orient|style|class|disabled|colspan)$/i;

function normStyle(v: string) {
  return v
    .replace(/\s+/g, "")
    .replace(/;$/, "")
    .split(";")
    .filter(Boolean)
    .sort()
    .join(";");
}

function ser(node: Node, react: boolean, depth = 0, out: string[] = []): string[] {
  const pad = "  ".repeat(depth);
  const kids: Node[] = [];
  // 인접한 글자 합치기
  let buf = "";
  const flush = () => {
    if (buf) {
      const t = buf.includes("\n") && !buf.trim() ? "" : buf;
      if (t) kids.push({ nodeType: 3, textContent: t } as unknown as Node);
      buf = "";
    }
  };
  for (const c of Array.from(node.childNodes)) {
    if (c.nodeType === 3) buf += c.textContent ?? "";
    else if (c.nodeType === 1) {
      flush();
      kids.push(c);
    }
  }
  flush();
  for (const c of kids) {
    if (c.nodeType === 3) {
      out.push(`${pad}"${(c.textContent ?? "").replace(/\s+/g, " ")}"`);
      continue;
    }
    const el = c as Element;
    const tag = el.tagName.toLowerCase();
    const cls = (el.getAttribute("class") ?? "")
      .split(/\s+/)
      .filter(Boolean)
      .map((x) => (react ? x.replace(/^_(.+)_[0-9a-z]+$/i, "$1") : x))
      .filter((x) => x !== "fresh")
      .sort();
    if (tag === "svg" && cls.includes("ic")) {
      out.push(`${pad}<ICON>`);
      continue;
    }
    const attrs = Array.from(el.attributes)
      .filter((a) => KEEP.test(a.name) && a.name !== "class")
      .map((a) => {
        let v = a.value;
        if (a.name === "style") v = normStyle(v);
        if (a.name === "hidden") v = "";
        return `${a.name.toLowerCase()}="${v}"`;
      })
      .sort();
    out.push(`${pad}<${tag}${cls.length ? ` .${cls.join(".")}` : ""}${attrs.length ? ` ${attrs.join(" ")}` : ""}>`);
    ser(el, react, depth + 1, out);
  }
  return out;
}

function orig() {
  const html = readFileSync(SRC, "utf8");
  const dom = new JSDOM(html, { runScripts: "dangerously", pretendToBeVisual: true, url: "http://localhost/" });
  return dom;
}

function dump(name: string, a: Node, b: Node) {
  writeFileSync(`${OUT}/${name}.orig.txt`, ser(a, false).join("\n"));
  writeFileSync(`${OUT}/${name}.react.txt`, ser(b, true).join("\n"));
}

it("compare", { timeout: 60000 }, async () => {
  // ---- Live ----
  useWb.setState(createInitialState());
  let dom = orig();
  let d = dom.window.document;
  const lsec = () => d.querySelector('section[data-view="live"]') as Element;
  let r = render(<Live />);
  dump("live-a-initial", lsec(), r.container);

  (d.querySelector("#lPause") as HTMLElement).click();
  fireEvent.click(r.container.querySelector("#lPause") as HTMLElement);
  dump("live-b-paused", lsec(), r.container);
  (d.querySelector("#lPause") as HTMLElement).click();
  fireEvent.click(r.container.querySelector("#lPause") as HTMLElement);

  (d.querySelector("#lEnd") as HTMLElement).click();
  fireEvent.click(r.container.querySelector("#lEnd") as HTMLElement);
  dump("live-c-confirm", lsec(), r.container);
  (d.querySelector("#lEndYes") as HTMLElement).click();
  fireEvent.click(r.container.querySelector("#lEndYes") as HTMLElement);
  dump("live-d-ended", lsec(), r.container);

  // 발견 고르기 (종료 상태)
  (d.querySelector('#lInsp [data-lsel="f1"]') as HTMLElement).click();
  fireEvent.click(r.container.querySelector('#lInsp [data-lsel="f1"]') as HTMLElement);
  dump("live-e-sel", lsec(), r.container);

  // 보고서: 종료한 점검으로
  (d.querySelector('#lDone a[data-rhost="live"]') as HTMLElement).click();
  const rsec = () => d.querySelector('section[data-view="report"]') as Element;
  cleanup();
  act(() => useWb.getState().setReportHost("live"));
  r = render(<Report />);
  dump("report-a-live-ended", rsec(), r.container);

  // ---- 새 원본: 보고서 blog ----
  dom = orig();
  d = dom.window.document;
  cleanup();
  useWb.setState(createInitialState());
  r = render(<Report />);
  dump("report-b-blog", rsec(), r.container);

  const both = (sel: string) => {
    const a = d.querySelector(sel) as HTMLElement;
    a.dispatchEvent(new dom.window.MouseEvent("click", { bubbles: true }));
    const b = r.container.querySelector(sel) as HTMLElement;
    fireEvent.click(b);
  };
  both("#rtab-graph");
  both('[data-kgn="rB"]');
  dump("report-c-node", rsec(), r.container);
  both('#rInsp [data-kgsel="eOrder"]');
  dump("report-d-rel", rsec(), r.container);
  both("[data-kgclear]");
  dump("report-e-clear", rsec(), r.container);
  both('[data-kgp="p3"]');
  dump("report-f-path", rsec(), r.container);
  both('[data-kgb="f2"]');
  dump("report-g-badge", rsec(), r.container);
  both("#rtab-list");
  both('[data-rsel="f4"]');
  dump("report-h-row", rsec(), r.container);

  // ---- 끝까지 재생 (원본은 실제 시간으로) ----
  dom = orig();
  d = dom.window.document;
  cleanup();
  useWb.setState(createInitialState());
  r = render(<Live />);
  dom.window.location.hash = "live";
  await new Promise((res) => setTimeout(res, 15500));
  act(() => {
    for (let n = 0; n < 50 && !useWb.getState().live.done; n++) useWb.getState().liveStep();
  });
  dump("live-f-done", lsec(), r.container);
  // 새 경로 제외
  (d.querySelector('[data-pathd="ex"]') as HTMLElement | null)?.click();
  dump("live-g-done-noPath", lsec(), r.container);
  cleanup();
});
