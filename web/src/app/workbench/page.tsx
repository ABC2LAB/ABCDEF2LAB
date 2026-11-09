import type { Metadata } from "next";

import { Workbench } from "@/workbench";

// 재상 작업대(/workbench). 팀 화면(사이트 루트)과 분리된 개인 작업 공간이다.
// 기본은 시안 데이터 재생(백엔드를 부르지 않음)이고, '실제 엔진' 으로 바꾸면 /api/* 를 부른다(작업대 README).
export const metadata: Metadata = {
  title: "작업대 · ABC2LAB",
};

export default function Page() {
  return <Workbench />;
}
