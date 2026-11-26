// #677 리뷰의 합성 fixture: 원문 가림(maskSecrets)만으로는 못 잡고, Markdown · HTML 로 그리면 비밀이 보이는 모양.
// 화면은 가리지 않으므로(DLP 폐기) 클립보드 복사(maskForCopy) 시험만 이 목록을 쓴다(#709 이동찬 리뷰 · #975). 값은 전부 합성이다.

/** [이름, 원문, 클립보드에 남으면 안 되는 합성 값] */
export const MARKUP_HIDDEN_SECRETS: [string, string, string][] = [
  ["HTML 엔티티", "&#112;&#97;&#115;&#115;&#119;&#111;&#114;&#100;=synthetic-entity-secret", "synthetic-entity-secret"],
  ["서식으로 쪼갬", "**pass**word=synthetic-split-secret", "synthetic-split-secret"],
  ["HTML 주석으로 쪼갬", "pass<!-- x -->word=synthetic-comment-secret", "synthetic-comment-secret"],
  ["역슬래시", "password\\=synthetic-escape-secret", "synthetic-escape-secret"],
  // #709 이동찬 리뷰 반례: 풀면 줄이 갈려 'password:' 와 값이 다른 줄이 된다
  ["엔티티 줄바꿈으로 키 · 값 가름", "password:&#10;synthetic-private-value-27", "synthetic-private-value-27"],
  ["표 칸", "| 이름 | 값 |\n| --- | --- |\n| a | api_key=synthetic-table-secret |", "synthetic-table-secret"],
  [
    "직접 이미지 대체 글",
    "![&#112;&#97;&#115;&#115;&#119;&#111;&#114;&#100;&#61;synthetic-image-secret](/synthetic.png)",
    "synthetic-image-secret",
  ],
  [
    "참조형 이미지 대체 글",
    "![&#112;&#97;&#115;&#115;&#119;&#111;&#114;&#100;&#61;synthetic-reference-mask][proof]\n\n[proof]: /synthetic.png",
    "synthetic-reference-mask",
  ],
];
