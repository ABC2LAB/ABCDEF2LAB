import "@testing-library/jest-dom/vitest";

import { cleanup, render } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";

import { useScrollTopOnChange, vulnClassParts } from "../ui/parts";

afterEach(cleanup);

describe("취약점 유형 딱지 나누기 (사용자 11/24: 긴 유형이 한 딱지에서 꺾여 테두리가 깨졌다)", () => {
  it("CWE 앞 쉼표에서만 나누고 괄호 안 쉼표는 그대로 둔다", () => {
    expect(
      vulnClassParts(
        "CWE-200 (Sensitive Information Exposure), CWE-287 (Improper Authentication), CWE-262 (Use of Password Hash Instead of Password)",
      ),
    ).toEqual([
      "CWE-200 (Sensitive Information Exposure)",
      "CWE-287 (Improper Authentication)",
      "CWE-262 (Use of Password Hash Instead of Password)",
    ]);
    expect(vulnClassParts("idor")).toEqual(["idor"]);
    expect(vulnClassParts("권한 우회 (읽기, 쓰기)")).toEqual(["권한 우회 (읽기, 쓰기)"]);
  });
});

function Panel({ pick }: { pick: string | null }) {
  const ref = useScrollTopOnChange<HTMLDivElement>(pick);
  return <div ref={ref} data-testid="panel" />;
}

describe("오른쪽 패널 맨 위로 (사용자 11/24: 내려 둔 채 다른 취약점을 누르면 아래에서 시작했다)", () => {
  it("고른 항목이 바뀔 때만 맨 위로 올리고, 같은 항목을 다시 그리면 그대로 둔다", () => {
    const { getByTestId, rerender } = render(<Panel pick="a" />);
    const panel = getByTestId("panel");
    panel.scrollTop = 200;
    rerender(<Panel pick="a" />);
    expect(panel.scrollTop).toBe(200);
    rerender(<Panel pick="b" />);
    expect(panel.scrollTop).toBe(0);
    panel.scrollTop = 120;
    rerender(<Panel pick={null} />);
    expect(panel.scrollTop).toBe(0);
  });
});
