import { describe, expect, it } from "vitest";

import { mapApiAccessMap } from "./kg";
import { mockFetch } from "./mock-data";

// 목업 빌드(NEXT_PUBLIC_MOCK=1)는 기본 task 1005 를 선택해 둔다. getAccessMap 이 taskId 로 조회하므로
// ROUTES 에 access-map 이 없으면 데모 지도가 로드 오류로 깨진다(회귀 방지).
describe("mock-data access-map 라우트", () => {
  it("taskId 있는 목업 조회가 계약 shape 를 돌려주고 mapApiAccessMap 이 매핑한다", async () => {
    const res = await mockFetch<Parameters<typeof mapApiAccessMap>[0]>("/api/tasks/1005/access-map");
    const map = mapApiAccessMap(res);
    expect(map.cells.length).toBeGreaterThan(0);
    expect(map.policyVersion).toMatch(/^kg-fixture\/v1:/);
    // confirmed→vuln 이 적어도 하나(지도에 확정 셀이 뜨는 경로 확인).
    expect(map.cells.some((c) => c.decision === "vuln")).toBe(true);
    // 마스킹(절대규칙 2): note 에 값·자격·URL·표식 값이 없다.
    for (const c of map.cells) expect(c.note ?? "").not.toMatch(/https?:|Bearer|kgfx-canary/i);
  });
});
