import "@testing-library/jest-dom/vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";

vi.mock("@/lib/diagnosis/api", () => ({ apiFetch: vi.fn() }));

import { apiFetch } from "@/lib/diagnosis/api";
import { catalog, externalModel } from "@/lib/diagnosis/diagnosis-url-fixtures";

import { ExternalModelSettings } from "../views/live/ExternalModelSettings";

const profile = { ...catalog.profiles[0], model: externalModel };
beforeEach(() => {
  vi.resetAllMocks();
  localStorage.clear();
  sessionStorage.clear();
});
afterEach(cleanup);

it("입력한 모델·키는 인증 설정 요청 한 번에만 사용하고 입력을 즉시 비운다", async () => {
  const ready = vi.fn();
  vi.mocked(apiFetch).mockResolvedValue({ configured: true });
  render(<ExternalModelSettings profile={profile} disabled={false} onReady={ready} />);
  const key = screen.getByLabelText("API 키");
  const secret = "synthetic-key-never-persist";
  fireEvent.change(key, { target: { value: secret } });
  fireEvent.click(screen.getByRole("button", { name: "모델 연결 설정 저장" }));
  expect(key).toHaveValue("");
  await waitFor(() => expect(ready).toHaveBeenLastCalledWith(true));
  expect(apiFetch).toHaveBeenCalledExactlyOnceWith(
    "/api/product/model-credentials",
    {
      method: "POST",
      redirect: "error",
      body: JSON.stringify({ profile_id: profile.id, model: externalModel.model, api_key: secret }),
    },
    false,
  );
  expect(document.body.textContent).not.toContain(secret);
  expect(JSON.stringify(localStorage)).not.toContain(secret);
  expect(JSON.stringify(sessionStorage)).not.toContain(secret);
  fireEvent.change(screen.getByLabelText("모델명"), { target: { value: "changed" } });
  expect(ready).toHaveBeenLastCalledWith(false);
});

it("설정 실패 응답의 키와 내부 상세를 화면에 노출하거나 자동 재전송하지 않는다", async () => {
  const ready = vi.fn();
  vi.mocked(apiFetch).mockRejectedValue(new Error("secret-provider-key/internal-error"));
  render(<ExternalModelSettings profile={profile} disabled={false} onReady={ready} />);
  fireEvent.change(screen.getByLabelText("API 키"), { target: { value: "synthetic-key" } });
  fireEvent.click(screen.getByRole("button", { name: "모델 연결 설정 저장" }));
  await screen.findByText(/키를 등록하지 못했습니다/);
  expect(apiFetch).toHaveBeenCalledTimes(1);
  expect(screen.getByLabelText("API 키")).toHaveValue("");
  expect(document.body.textContent).not.toContain("secret-provider-key");
  expect(ready).toHaveBeenLastCalledWith(false);
});
