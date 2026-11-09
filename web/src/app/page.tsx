"use client";

import ABC2LabUI from "@/abc2lab-ui";

// 실제 서비스는 CLI 첫 진단 연결로 진입한다. 목업 빌드는 기존 예시 화면을 유지한다.
export default function Page() {
  return <ABC2LabUI mode="first-diagnosis" initialScreen="entry" />;
}
