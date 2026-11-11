import "@testing-library/jest-dom/vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";

import { downloadDiagnostic } from "../live/api";
import { Download } from "../views/live/UrlLiveLive";

vi.mock("../live/api", async (original) => ({
  ...(await original<typeof import("../live/api")>()),
  downloadDiagnostic: vi.fn(),
}));
afterEach(() => {
  cleanup();
  vi.resetAllMocks();
});
it("shows progress and completion on the chosen Word download", async () => {
  let finish!: () => void;
  vi.mocked(downloadDiagnostic).mockImplementation(
    () =>
      new Promise<void>((resolve) => {
        finish = resolve;
      }),
  );
  render(<Download taskId="42" />);
  fireEvent.click(screen.getByRole("button", { name: "Word 공유용" }));
  expect(screen.getByRole("button", { name: "Word 공유용 내려받는 중…" })).toBeDisabled();
  expect(screen.getByRole("button", { name: "공유용 사본" })).toBeDisabled();
  finish();
  expect(await screen.findByRole("status")).toHaveTextContent("Word 공유용 파일을 저장했습니다.");
});
