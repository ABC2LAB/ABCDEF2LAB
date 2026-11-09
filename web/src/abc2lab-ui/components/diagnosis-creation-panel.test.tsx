import "@testing-library/jest-dom/vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { ApiError } from "../lib/api";
import { readURLCatalog } from "../lib/diagnosis-url";
import { catalog } from "../lib/diagnosis-url-fixtures";
import { DiagnosisCreationPanel } from "./DiagnosisCreationPanel";

vi.mock("../lib/diagnosis-url", async (original) => ({
  ...(await original<typeof import("../lib/diagnosis-url")>()),
  readURLCatalog: vi.fn(),
}));
vi.mock("./NewDiagnosisPanel", () => ({ NewDiagnosisPanel: () => <p>기존 v1 초안</p> }));
vi.mock("./URLDiagnosisPanel", () => ({ URLDiagnosisPanel: () => <p>URL v2 초안</p> }));
beforeEach(() => {
  vi.resetAllMocks();
});
afterEach(cleanup);
describe("읽기 전용 기능 선택", () => {
  it("정상 v2 catalog만 v2 입력을 연다", async () => {
    vi.mocked(readURLCatalog).mockResolvedValue(catalog);
    render(<DiagnosisCreationPanel />);
    expect(await screen.findByText("URL v2 초안")).toBeVisible();
    expect(screen.queryByText("기존 v1 초안")).toBeNull();
  });
  it.each([404, 501])("정확한 미지원 %i만 기존 v1을 조회한다", async (status) => {
    vi.mocked(readURLCatalog).mockRejectedValue(new ApiError(status, "unsupported"));
    render(<DiagnosisCreationPanel />);
    expect(await screen.findByText("기존 v1 초안")).toBeVisible();
    expect(screen.queryByText("URL v2 초안")).toBeNull();
  });
  it.each([401, 403, 422, 500, 503])("권한·응답 오류 %i를 다른 API로 우회하지 않는다", async (status) => {
    vi.mocked(readURLCatalog).mockRejectedValue(new ApiError(status, "blocked"));
    render(<DiagnosisCreationPanel />);
    await screen.findByRole("alert");
    expect(screen.queryByText("기존 v1 초안")).toBeNull();
    expect(screen.queryByText("URL v2 초안")).toBeNull();
  });
});
