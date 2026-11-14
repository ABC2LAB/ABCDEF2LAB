import "@testing-library/jest-dom/vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, it } from "vitest";

import { scope } from "@/lib/diagnosis/diagnosis-url-fixtures";

import { ApprovedWriteNotice } from "../views/live/ApprovedWriteNotice";

afterEach(cleanup);
it("실제 승인 origin·경로·메서드와 예측 불가능한 쓰기·상태 변경을 고지한다", () => {
  render(
    <ApprovedWriteNotice
      scope={{ ...scope, path_prefixes: ["/owned", "/api/safe"], methods: ["GET", "POST", "PATCH"] }}
    />,
  );
  expect(screen.getByText(scope.origin)).toBeInTheDocument();
  expect(screen.getByText("/owned, /api/safe")).toBeInTheDocument();
  expect(screen.getByText("GET / POST / PATCH")).toBeInTheDocument();
  expect(screen.getByRole("note")).toHaveTextContent(
    "어떤 쓰기 요청이 나갈지 미리 모두 알 수 없으며, 대상 데이터나 상태가 바뀔 수 있습니다",
  );
  expect(screen.getByText(/자동 모드:/)).toHaveTextContent("요청마다 수동 확인하지 않고 보냅니다");
});
it("C21 자동 범위 승인과 요청별 수동 승인을 구분한다", () => {
  render(
    <ApprovedWriteNotice scope={{ ...scope, methods: ["GET", "POST"], approval_mode: "workbench_selective_v1" }} />,
  );
  expect(screen.getByText(/사용자 승인 모드:/)).toHaveTextContent("요청마다 사람이 내용을 확인하고 승인한 뒤");
  expect(screen.queryByText(/수동 확인하지 않고/)).toBeNull();
});
it.each([
  undefined,
  { ...scope, methods: ["GET", "HEAD"] },
  { ...scope, methods: [] },
  { ...scope, path_prefixes: [], methods: ["POST"] },
  { ...scope, methods: ["DELETE"] },
  { ...scope, methods: ["POST"], approval_mode: "confirm_every_request" as const },
])("실제 쓰기 범위가 없으면 승인된 쓰기를 표시하지 않는다", (value) => {
  render(<ApprovedWriteNotice scope={value} />);
  expect(screen.queryByRole("region", { name: "쓰기 승인 안내" })).toBeNull();
  expect(screen.queryByText("승인된 쓰기 범위")).toBeNull();
});
