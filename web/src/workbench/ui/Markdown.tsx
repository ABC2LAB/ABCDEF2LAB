"use client";

import ReactMarkdown, { type Components } from "react-markdown";
import remarkGfm from "remark-gfm";

import { maskSecrets } from "../live/mask";
import s from "../workbench.module.css";

/**
 * 링크는 글로만, 이미지는 불러오지 않고 대체 글로만 보인다. 보고 글 속 주소를 누르거나 그리다가 브라우저가 승인 대상 밖 ·
 * 외부로 나가지 않게 한다(절대규칙 1 · 8, 완전 로컬 모드는 외부 송신을 막는다).
 */
const components: Components = {
  a: ({ children }) => <span className={s["md-link"]}>{children}</span>,
  img: ({ alt }) => <span className={s["md-img"]}>{alt ? `[이미지: ${alt}]` : "[이미지]"}</span>,
};

/** 가림에 필요한 만큼만 본 Markdown 구문 나무 마디 (mdast). 의존성을 늘리지 않으려고 직접 적는다. */
type MdNode = { type: string; value?: string; alt?: string | null; children?: MdNode[] };

/** 글자를 줄 단위로 이어 붙여 보이는 마디: 이 안에서 서식 · 주석으로 쪼개진 글도 한 덩어리로 가린다. */
const BLOCKS = new Set(["paragraph", "heading", "tableCell"]);
/** 대체 글(alt)이 화면에 보이는 마디: 직접 이미지 `![글](주소)` · 참조형 이미지 `![글][이름]` (#677 리뷰). */
const IMAGES = new Set(["image", "imageReference"]);

/** 화면에 보일 글(원문 HTML 은 그리지 않으므로 뺀다). 엔티티 · 역슬래시 풀기는 파서가 이미 했다. */
function visibleText(node: MdNode): string {
  if (node.type === "html") return "";
  if (node.type === "text" || node.type === "inlineCode") return node.value ?? "";
  if (IMAGES.has(node.type)) return node.alt ?? "";
  if (node.type === "break") return "\n";
  return (node.children ?? []).map(visibleText).join("");
}

function maskTree(node: MdNode): void {
  if (node.type === "code" || node.type === "text" || node.type === "inlineCode") {
    if (node.value) node.value = maskSecrets(node.value);
  }
  if (IMAGES.has(node.type) && node.alt) node.alt = maskSecrets(node.alt);
  if (BLOCKS.has(node.type)) {
    const shown = visibleText(node);
    const masked = maskSecrets(shown);
    // 서식(**pass**word) · 주석(pass<!-- -->word) 으로 쪼개져 마디마다 가리면 놓치는 값: 블록 글 전체로 다시 보고, 가릴 게
    // 있으면 그 블록은 서식을 버리고 가린 글 한 줄로 바꾼다
    if (masked !== shown) {
      node.children = [{ type: "text", value: masked }];
      return;
    }
  }
  for (const child of node.children ?? []) maskTree(child);
}

/**
 * 파싱 뒤 가림 (#677 이동찬 리뷰): 원문에 먼저 maskSecrets 를 걸어도 파서가 HTML 엔티티(&#112;…) · 역슬래시를 풀면서 평문
 * 비밀이 되살아난다. 그래서 파서가 푼 구문 나무의 글에 다시 건다.
 */
function remarkMaskSecrets() {
  return (tree: unknown) => {
    maskTree(tree as MdNode);
  };
}

/**
 * 서버가 저장한 Markdown 글을 읽기 전용으로 그린다(ARTEX 발견 상세의 '상세 보고'처럼). 원문 HTML 은 그리지 않는다(skipHtml).
 * 가림은 두 번: 부르는 쪽이 원문에(maskSecrets), 여기서 파서가 푼 글에(remarkMaskSecrets).
 */
export function Markdown({ text }: { text: string }) {
  return (
    <div className={s.md}>
      <ReactMarkdown remarkPlugins={[remarkGfm, remarkMaskSecrets]} components={components} skipHtml>
        {text}
      </ReactMarkdown>
    </div>
  );
}
