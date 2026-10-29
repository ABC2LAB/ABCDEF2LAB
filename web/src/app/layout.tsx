import type { Metadata } from "next";
import type { ReactNode } from "react";

// ABC2LAB 점검 흐름 UI 전용 전역 스타일(원본 시안 그대로). 기존 globals.css(Tailwind/shadcn)는
// :root·body 를 덮어써 시안과 충돌하므로 여기서 불러오지 않는다.
import "@/abc2lab-ui/abc2lab.css";

export const metadata: Metadata = {
  title: "ABC2LAB 점검 흐름",
};

// TODO(이경준): Noto Sans KR 정확 렌더는 폐쇄망에서 외부 요청 없이 쓰도록 빌드타임 self-host
// (next/font 로컬 또는 public/)로 붙인다. 지금은 abc2lab.css 의 한글 폴백 폰트로 뜬다.
export default function RootLayout({ children }: Readonly<{ children: ReactNode }>) {
  return (
    <html lang="ko">
      <body>{children}</body>
    </html>
  );
}
