import type { Metadata } from "next";

import { Workbench } from "@/workbench";

// /workbench 는 사이트 루트(/)와 같은 작업대를 보인다(예전 주소 호환). 모든 값은 /api/* 실제 서버 응답이다.
export const metadata: Metadata = {
  title: "작업대 · JinDo",
};

export default function Page() {
  return <Workbench />;
}
