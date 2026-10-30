import "@testing-library/jest-dom/vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { CopyButton } from "./CopyButton";

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});
describe("clipboard feedback", () => {
  it("waits for the clipboard write before showing success", async () => {
    let finish!: () => void;
    const writeText = vi.fn(
      () =>
        new Promise<void>((resolve) => {
          finish = resolve;
        }),
    );
    vi.stubGlobal("navigator", { clipboard: { writeText } });
    render(<CopyButton text="example" />);
    fireEvent.click(screen.getByRole("button"));
    expect(writeText).toHaveBeenCalledWith("example");
    expect(screen.queryByText("복사됨")).toBeNull();
    finish();
    expect(await screen.findByText("복사됨")).toBeVisible();
  });
  it("shows failure when clipboard permission is denied", async () => {
    vi.stubGlobal("navigator", { clipboard: { writeText: vi.fn().mockRejectedValue(new Error("denied")) } });
    render(<CopyButton text="example" />);
    fireEvent.click(screen.getByRole("button"));
    await waitFor(() => expect(screen.getByText("복사 실패")).toBeVisible());
    expect(screen.queryByText("복사됨")).toBeNull();
  });
});
