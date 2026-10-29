"use client";

import ABC2LabUI from "@/abc2lab-ui";

// 사이트 루트 = ABC2LAB 점검 흐름 시안. 12개 화면을 모두 마운트하고 해시(#report 등)로 전환한다.
// 숫자·목록은 아직 시안 데이터 — 2단계에서 화면별로 Go API(/api/*)에 배선한다.
export default function Page() {
  return <ABC2LabUI />;
}
