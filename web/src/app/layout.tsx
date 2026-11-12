import type { ReactNode } from "react";

import type { Metadata } from "next";

// 폐쇄망에서 외부 요청 없이 한글이 뜨도록 Noto Sans KR 을 self-host (fontsource, 빌드타임 번들).
// variable 축이라 한 패밀리("Noto Sans KR Variable")로 전 weight 커버, unicode-range 로 필요한 글자 청크만 로드.
import "@fontsource-variable/noto-sans-kr/index.css";
// 작업대 시안(v91)의 글꼴. 시안은 Google Fonts 로 받지만 폐쇄망이라 같은 글꼴을 self-host 한다(OFL-1.1).
// 시안과 같은 굵기만: 본문 400 · 500 · 600 · 700, 고정폭 400 · 500 · 600.
import "@fontsource/ibm-plex-sans-kr/400.css";
import "@fontsource/ibm-plex-sans-kr/500.css";
import "@fontsource/ibm-plex-sans-kr/600.css";
import "@fontsource/ibm-plex-sans-kr/700.css";
import "@fontsource/ibm-plex-mono/400.css";
import "@fontsource/ibm-plex-mono/500.css";
import "@fontsource/ibm-plex-mono/600.css";
// ABC2LAB 점검 흐름 UI 전용 전역 스타일(원본 시안 그대로). 기존 globals.css(Tailwind/shadcn)는
// :root·body 를 덮어써 시안과 충돌하므로 여기서 불러오지 않는다.
import "./globals.css";

export const metadata: Metadata = {
  title: "JinDo",
  applicationName: "JinDo",
};

export default function RootLayout({ children }: Readonly<{ children: ReactNode }>) {
  return (
    <html lang="ko">
      <body>{children}</body>
    </html>
  );
}
