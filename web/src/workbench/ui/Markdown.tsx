"use client";

import ReactMarkdown, { type Components } from "react-markdown";
import remarkGfm from "remark-gfm";

import s from "../workbench.module.css";

/**
 * 링크는 글로만, 이미지는 불러오지 않고 대체 글로만 보인다. 보고 글 속 주소를 누르거나 그리다가 브라우저가 승인 대상 밖 ·
 * 외부로 나가지 않게 한다(절대규칙 1 · 8, 완전 로컬 모드는 외부 송신을 막는다).
 */
const components: Components = {
  a: ({ children }) => <span className={s["md-link"]}>{children}</span>,
  img: ({ alt }) => <span className={s["md-img"]}>{alt ? `[이미지: ${alt}]` : "[이미지]"}</span>,
};

/**
 * 서버가 저장한 Markdown 글을 읽기 전용으로 그린다(ARTEX 발견 상세의 '상세 보고'처럼). 원문 HTML 은 그리지 않는다(skipHtml).
 * 글은 가리지 않는다(DLP 폐기, CLAUDE.md 절대규칙 2 — 제품 화면은 내부 담당자만 본다).
 */
export function Markdown({ text }: { text: string }) {
  return (
    <div className={s.md}>
      <ReactMarkdown remarkPlugins={[remarkGfm]} components={components} skipHtml>
        {text}
      </ReactMarkdown>
    </div>
  );
}
