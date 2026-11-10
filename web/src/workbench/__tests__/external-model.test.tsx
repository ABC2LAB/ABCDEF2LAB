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
  expect(apiFetch).toHaveBeenCalledWith(
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
  expect(screen.getByLabelText("모델명")).toHaveAttribute("readonly");
  fireEvent.change(key, { target: { value: "replacement-key" } });
  expect(ready).toHaveBeenLastCalledWith(false);
});

it("설정 실패 응답의 키와 내부 상세를 화면에 노출하거나 자동 재전송하지 않는다", async () => {
  const ready = vi.fn();
  vi.mocked(apiFetch).mockRejectedValue(new Error("secret-provider-key/internal-error"));
  render(<ExternalModelSettings profile={profile} disabled={false} onReady={ready} />);
  fireEvent.change(screen.getByLabelText("API 키"), { target: { value: "synthetic-key" } });
  fireEvent.click(screen.getByRole("button", { name: "모델 연결 설정 저장" }));
  await screen.findByText(/키를 등록하지 못했습니다/);
  expect(vi.mocked(apiFetch).mock.calls.filter(([, init]) => init?.method === "POST")).toHaveLength(1);
  expect(screen.getByLabelText("API 키")).toHaveValue("");
  expect(document.body.textContent).not.toContain("secret-provider-key");
  expect(ready).toHaveBeenLastCalledWith(false);
});

it("다음 진단은 저장된 키의 존재만 조회하고 비밀 없이 재사용한다", async () => {
  const ready = vi.fn();
  vi.mocked(apiFetch).mockResolvedValue({ credential: { configured: true, model: externalModel.model } });
  const first = render(<ExternalModelSettings profile={profile} disabled={false} onReady={ready} />);
  await screen.findByText(/등록된 API 키를 재사용/);
  expect(ready).toHaveBeenLastCalledWith(true);
  expect(screen.getByLabelText("API 키")).toHaveValue("");
  first.unmount();
  render(<ExternalModelSettings profile={profile} disabled={false} onReady={ready} />);
  await screen.findByText(/등록된 API 키를 재사용/);
  expect(ready).toHaveBeenLastCalledWith(true);
  expect(vi.mocked(apiFetch).mock.calls).toHaveLength(2);
  expect(vi.mocked(apiFetch).mock.calls.every(([, init]) => !init?.method && !init?.body)).toBe(true);
  expect(localStorage.length).toBe(0);
  expect(sessionStorage.length).toBe(0);
});

it("다른 모델의 등록 상태로 준비 완료를 만들지 않는다", async () => {
  const ready = vi.fn();
  vi.mocked(apiFetch).mockResolvedValue({ credential: { configured: true, model: "unapproved-model" } });
  render(<ExternalModelSettings profile={profile} disabled={false} onReady={ready} />);
  await screen.findByText("API 키를 한 번 등록하세요.");
  expect(ready).toHaveBeenLastCalledWith(false);
});
