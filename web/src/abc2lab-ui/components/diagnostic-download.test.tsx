import "@testing-library/jest-dom/vitest";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";

import { apiFetchBlob } from "../lib/api";
import { DiagnosticDownload } from "./DiagnosticDownload";

vi.mock("../lib/api", () => ({ apiFetchBlob: vi.fn() }));
let click: ReturnType<typeof vi.spyOn>;
beforeEach(() => {
  vi.mocked(apiFetchBlob).mockResolvedValue(new Blob(["safe"]));
  URL.createObjectURL = vi.fn(() => "blob:report");
  URL.revokeObjectURL = vi.fn();
  click = vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => {
    /* suppress browser navigation in jsdom */
  });
});
afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  vi.clearAllMocks();
});
it("defaults to shared, uses the exact task and only downloads a local blob", async () => {
  render(<DiagnosticDownload taskId="42" enabled />);
  fireEvent.click(screen.getByText("종합 HTML 다운로드"));
  await waitFor(() => expect(click).toHaveBeenCalledOnce());
  expect(apiFetchBlob).toHaveBeenCalledWith(
    "/api/report/diagnostic?task=42&audience=shared&download=1",
    expect.objectContaining({ signal: expect.any(AbortSignal) }),
  );
  expect(document.querySelector("a")).toBeNull();
});
it("requires explicit internal selection", async () => {
  render(<DiagnosticDownload taskId="42" enabled />);
  fireEvent.change(screen.getByRole("combobox"), { target: { value: "internal" } });
  fireEvent.click(screen.getByText("종합 HTML 다운로드"));
  await waitFor(() => expect(click).toHaveBeenCalledOnce());
  expect(vi.mocked(apiFetchBlob).mock.calls[0][0]).toContain("audience=internal");
});
it.each(["cancel", "navigate"])("suppresses a delayed download after %s and prevents repeated clicks", async (mode) => {
  let resolve!: (b: Blob) => void;
  vi.mocked(apiFetchBlob).mockReturnValue(
    new Promise((yes) => {
      resolve = yes;
    }),
  );
  const view = render(<DiagnosticDownload taskId="42" enabled />);
  fireEvent.click(screen.getByText("종합 HTML 다운로드"));
  fireEvent.click(screen.getByText("보고서 생성 중…"));
  expect(apiFetchBlob).toHaveBeenCalledOnce();
  if (mode === "cancel") fireEvent.click(screen.getByText("취소"));
  else view.rerender(<DiagnosticDownload taskId="42" enabled={false} />);
  await act(async () => resolve(new Blob(["old"])));
  expect(click).not.toHaveBeenCalled();
});
it("does not download invalid tasks or failed responses", async () => {
  const view = render(<DiagnosticDownload taskId="../other" enabled />);
  expect(screen.getByText("종합 HTML 다운로드")).toBeDisabled();
  view.rerender(<DiagnosticDownload taskId="42" enabled />);
  vi.mocked(apiFetchBlob).mockRejectedValue(new Error("secret server text"));
  fireEvent.click(screen.getByText("종합 HTML 다운로드"));
  await screen.findByRole("status");
  expect(screen.queryByText("secret server text")).toBeNull();
  expect(click).not.toHaveBeenCalled();
});

it.each(["task", "unmount"])("suppresses old responses on %s change", async (mode) => {
  let resolve!: (b: Blob) => void;
  vi.mocked(apiFetchBlob).mockReturnValue(
    new Promise((yes) => {
      resolve = yes;
    }),
  );
  const view = render(<DiagnosticDownload key="42" taskId="42" enabled />);
  fireEvent.click(screen.getByText("종합 HTML 다운로드"));
  if (mode === "task") view.rerender(<DiagnosticDownload key="43" taskId="43" enabled />);
  else view.unmount();
  await act(async () => resolve(new Blob(["old"])));
  expect(click).not.toHaveBeenCalled();
});
it("old cancellation cannot finish a newer download and filenames are local", async () => {
  let old!: (b: Blob) => void;
  let fresh!: (b: Blob) => void;
  vi.mocked(apiFetchBlob)
    .mockReturnValueOnce(
      new Promise((yes) => {
        old = yes;
      }),
    )
    .mockReturnValueOnce(
      new Promise((yes) => {
        fresh = yes;
      }),
    );
  render(<DiagnosticDownload taskId="42" enabled />);
  fireEvent.click(screen.getByText("종합 HTML 다운로드"));
  fireEvent.click(screen.getByText("취소"));
  fireEvent.click(screen.getByText("종합 HTML 다운로드"));
  await act(async () => old(new Blob(["old"])));
  expect(screen.getByText("보고서 생성 중…")).toBeDisabled();
  click.mockImplementation(function (this: HTMLAnchorElement) {
    expect(this.download).toBe("diagnostic-task-42-shared.html");
    expect(this.href).toBe("blob:report");
    expect(this.target).toBe("");
  });
  vi.useFakeTimers();
  try {
    await act(async () => fresh(new Blob(["fresh"])));
    expect(click).toHaveBeenCalledOnce();
    act(() => vi.advanceTimersByTime(1000));
    expect(URL.revokeObjectURL).toHaveBeenCalledWith("blob:report");
  } finally {
    vi.useRealTimers();
  }
});
