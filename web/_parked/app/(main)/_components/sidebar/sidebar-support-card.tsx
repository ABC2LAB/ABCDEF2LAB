// T1.2(임시): 템플릿 잔재 외부 링크 서포트 카드. 삭제 대신 본문 전체를 주석 처리한다 — 결정 후 복원/제거.
// 복원: 아래 블록 주석(/* ... */)을 풀고 이 export {} 줄을 지운다. app-sidebar.tsx 의 import 주석도 함께 해제.
export {};
/*
import Link from "next/link";

import { siX } from "simple-icons";

import { SimpleIcon } from "@/components/simple-icon";
import { Card, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";

export function SidebarSupportCard() {
  return (
    <Card size="sm" className="overflow-hidden shadow-none group-data-[collapsible=icon]:hidden">
      <CardHeader className="min-w-0 px-4">
        <CardTitle className="truncate text-sm">Looking for something more?</CardTitle>
        <CardDescription className="line-clamp-2">
          Open an issue or do reach out to me on&nbsp;
          <Link
            href="https://x.com/arhamkhnz"
            target="_blank"
            rel="noreferrer"
            aria-label="Reach out on X"
            className="inline-flex items-center text-foreground"
          >
            <SimpleIcon icon={siX} aria-hidden className="size-3 fill-current" />
          </Link>
          .
        </CardDescription>
      </CardHeader>
    </Card>
  );
}
*/
