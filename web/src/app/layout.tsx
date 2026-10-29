import type { Metadata } from "next";
import type { ReactNode } from "react";

// 폐쇄망에서 외부 요청 없이 한글이 뜨도록 Noto Sans KR 을 self-host (fontsource, 빌드타임 번들).
// variable 축이라 한 패밀리("Noto Sans KR Variable")로 전 weight 커버, unicode-range 로 필요한 글자 청크만 로드.
import "@fontsource-variable/noto-sans-kr/index.css";
// ABC2LAB 점검 흐름 UI 전용 전역 스타일(원본 시안 그대로). 기존 globals.css(Tailwind/shadcn)는
// :root·body 를 덮어써 시안과 충돌하므로 여기서 불러오지 않는다.
import "@/abc2lab-ui/abc2lab.css";

export const metadata: Metadata = {
  title: "ABC2LAB 점검 흐름",
};

export default function RootLayout({ children }: Readonly<{ children: ReactNode }>) {
  return (
    <html lang="ko">
      <body>{children}</body>
    </html>
  );
}
