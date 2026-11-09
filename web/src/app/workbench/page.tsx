import type { Metadata } from "next";

import { Workbench } from "@/workbench";

// 재상 작업대(/workbench). 팀 화면(사이트 루트)과 분리된 개인 작업 공간이다.
// 아직 시안 데이터로 재생만 하며 백엔드(/api/*)는 부르지 않는다.
export const metadata: Metadata = {
  title: "작업대 · ABC2LAB",
};

export default function Page() {
  return <Workbench />;
}
