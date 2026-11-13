import "@testing-library/jest-dom/vitest";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, onTestFinished, vi } from "vitest";

vi.mock("@/lib/diagnosis/api", async (original) => ({
  ...(await original<typeof import("@/lib/diagnosis/api")>()),
  apiFetch: vi.fn(),
}));

import { ApiError, apiFetch } from "@/lib/diagnosis/api";

import { TargetApprovalPanel } from "../views/live/TargetApprovalPanel";

const pending = {
  id: `target-${"a".repeat(32)}`,
  origin: "https://owned.example.test",
  state: "pending",
  requests: 4,
  port: 443,
  challenge_name: "_abc2lab-challenge.owned.example.test",
  challenge_value: "synthetic-proof",
};
const approved = {
  ...pending,
  state: "approved",
  path_prefixes: ["/read"],
  methods: ["GET", "HEAD"],
  addresses: ["10.200.1.10"],
  tls_spki_sha256: "a".repeat(64),
  expires_at: "2099-01-01T00:00:00Z",
};
const url = `${pending.origin}/read`;
beforeEach(() => vi.resetAllMocks());
afterEach(cleanup);
it.each([
  "http://owned.example.test/read",
  "https://user:password@owned.example.test/read",
])("불가한 HTTPS origin은 조회·등록하지 않는다: %s", (url) => {
  render(<TargetApprovalPanel url={url} disabled={false} />);
  // 정본 HTTPS origin 이 아니면 대상 승인 칸을 아예 그리지 않는다 (등록 버튼 없음)
  expect(screen.queryByRole("button", { name: "대상 등록 · 소유 확인 준비" })).toBeNull();
  expect(screen.queryByRole("button", { name: "대상 등록하기" })).toBeNull();
  expect(apiFetch).not.toHaveBeenCalled();
});
it.each([404, 501])("API %s는 패널을 숨긴다", async (status) => {
  vi.mocked(apiFetch).mockRejectedValue(new ApiError(status, "unsupported"));
  render(<TargetApprovalPanel url={url} disabled={false} />);
  await waitFor(() => expect(screen.queryByText("대상 권한 · 운영자 승인")).toBeNull());
  expect(apiFetch).toHaveBeenCalledTimes(1);
});
it("등록 화면에 GET/HEAD · query 무해성을 서버가 검증하지 않는다는 고지가 보인다", async () => {
  vi.mocked(apiFetch).mockResolvedValueOnce({});
  render(<TargetApprovalPanel url={url} disabled={false} />);
  await waitFor(() => expect(apiFetch).toHaveBeenCalledTimes(1));
  fireEvent.click(screen.getByRole("button", { name: "대상 등록하기" }));
  expect(screen.getByText(/서버는 GET\/HEAD 의 무해성을 검증하지 않습니다/)).toBeVisible();
  expect(screen.getByText(/서버는 query 의 무해성을 검증하지 않으니/)).toBeVisible();
  expect(screen.getByText(/상태 변경 범위는 별도 동의한 설치에서만 등록할 수 있습니다/)).toBeVisible();
});
it("등록의 읽기 범위와 TLS 사전 접촉을 안내한다", async () => {
  vi.mocked(apiFetch).mockResolvedValueOnce({}).mockResolvedValueOnce({ target: pending });
  render(<TargetApprovalPanel url={url} disabled={false} />);
  await waitFor(() => expect(apiFetch).toHaveBeenCalledTimes(1));
  fireEvent.click(screen.getByRole("button", { name: "대상 등록하기" }));
  expect(screen.getByText(/후보 IP별 TLS 연결/)).toBeVisible();
  fireEvent.change(screen.getByLabelText("진단 권한 · 비변경 근거"), {
    target: { value: "승인된 테스트 소유 환경의 읽기 전용 GET 경로입니다." },
  });
  fireEvent.click(screen.getByRole("button", { name: "대상 등록 · 소유 확인 준비" }));
  await screen.findByRole("button", { name: "TXT 레코드 확인" });
  const [path, init, retry] = vi.mocked(apiFetch).mock.calls[1];
  expect(path).toBe("/api/product/targets");
  expect(init?.method).toBe("POST");
  expect(retry).toBe(false);
  expect(JSON.parse(String(init?.body))).toEqual({
    url,
    path_prefixes: ["/read"],
    methods: ["GET", "HEAD"],
    readonly_basis: "승인된 테스트 소유 환경의 읽기 전용 GET 경로입니다.",
    requests: 4,
    valid_hours: 4,
  });
  expect(screen.queryByText(/승인된 대상:/)).toBeNull();
});
it.each([
  "network-secret-detail",
  "__proto__",
  "constructor",
])("승인 결과 불명은 다시 보내지 않고 상태 재조회를 요구한다: %s", async (errorCode) => {
  const changed = vi.fn();
  vi.mocked(apiFetch)
    .mockResolvedValueOnce({ target: pending })
    .mockRejectedValueOnce(new TypeError(errorCode))
    .mockResolvedValueOnce({ target: approved });
  render(<TargetApprovalPanel url={url} disabled={false} onChanged={changed} />);
  const allow = await screen.findByRole("button", { name: "TXT 레코드 확인" });
  fireEvent.click(allow);
  await screen.findByText(/변경 결과를 확인하지 못했습니다/);
  expect(changed).not.toHaveBeenCalled();
  expect(allow).toBeDisabled();
  fireEvent.click(allow);
  expect(apiFetch).toHaveBeenCalledTimes(2);
  expect(screen.queryByText(/승인된 대상:/)).toBeNull();
  expect(document.body).not.toHaveTextContent(errorCode);
  fireEvent.click(screen.getByRole("button", { name: "대상 승인 다시 조회" }));
  await screen.findByRole("button", { name: "승인 철회" });
  expect(apiFetch).toHaveBeenCalledTimes(3);
  expect(changed).not.toHaveBeenCalled();
});
it("철회 후 기존 승인 표시를 제거하고 새 등록을 허용한다", async () => {
  const changed = vi.fn();
  vi.mocked(apiFetch)
    .mockResolvedValueOnce({ target: approved })
    .mockResolvedValueOnce({ target: { ...approved, state: "revoked" } });
  render(<TargetApprovalPanel url={url} disabled={false} onChanged={changed} />);
  fireEvent.click(await screen.findByRole("button", { name: "승인 철회" }));
  await screen.findByText(/승인을 철회했습니다/);
  expect(screen.queryByRole("button", { name: "승인 철회" })).toBeNull();
  expect(screen.queryByText(/승인된 대상:/)).toBeNull();
  fireEvent.click(screen.getByRole("button", { name: "대상 등록하기" }));
  expect(screen.getByRole("button", { name: "대상 등록 · 소유 확인 준비" })).toBeVisible();
  expect(changed).toHaveBeenCalledTimes(1);
});
it("다른 origin의 승인 응답은 적용하지 않는다", async () => {
  const changed = vi.fn();
  vi.mocked(apiFetch)
    .mockResolvedValueOnce({ target: pending })
    .mockResolvedValueOnce({ target: { ...approved, origin: "https://other.example.test" } });
  render(<TargetApprovalPanel url={url} disabled={false} onChanged={changed} />);
  fireEvent.click(await screen.findByRole("button", { name: "TXT 레코드 확인" }));
  await screen.findByText(/변경 결과를 확인하지 못했습니다/);
  expect(changed).not.toHaveBeenCalled();
  expect(screen.queryByText(/승인된 대상:/)).toBeNull();
});
it("중복 승인 클릭·URL 변경 뒤 늦은 응답이 새 대상에 영향을 주지 않는다", async () => {
  let finish: (value: unknown) => void = () => undefined;
  const held = new Promise((resolve) => {
    finish = resolve;
  });
  const changed = vi.fn();
  vi.mocked(apiFetch).mockResolvedValueOnce({ target: pending }).mockReturnValueOnce(held).mockResolvedValueOnce({});
  const view = render(<TargetApprovalPanel url={url} disabled={false} onChanged={changed} />);
  const allow = await screen.findByRole("button", { name: "TXT 레코드 확인" });
  fireEvent.click(allow);
  fireEvent.click(allow);
  expect(apiFetch).toHaveBeenCalledTimes(2);
  view.rerender(<TargetApprovalPanel url="https://new.example.test/read" disabled={false} onChanged={changed} />);
  await waitFor(() => expect(apiFetch).toHaveBeenCalledTimes(3));
  await act(async () => finish({ target: approved }));
  expect(changed).not.toHaveBeenCalled();
  expect(screen.queryByText(/승인된 대상:/)).toBeNull();
});

it("새 origin에는 이전 대상의 쓰기 동의가 넘어가지 않는다", async () => {
  vi.mocked(apiFetch).mockResolvedValue({});
  const view = render(<TargetApprovalPanel url={url} disabled={false} allowStateChanges />);
  await waitFor(() => expect(apiFetch).toHaveBeenCalledTimes(1));
  fireEvent.click(screen.getByRole("button", { name: "대상 등록하기" }));
  // 쓰기 동의는 AI 자동 모드가 건별 확인 없이 보낸다는 뜻까지 보인다(C21, #525 이경준 리뷰)
  expect(screen.getByRole("checkbox").closest("label")).toHaveTextContent(/AI 자동 모드는 건별 확인 없이 보냄/);
  fireEvent.click(screen.getByRole("checkbox"));
  expect(screen.getByRole("checkbox")).toBeChecked();
  view.rerender(<TargetApprovalPanel url="https://second.example.test/read" disabled={false} allowStateChanges />);
  await waitFor(() => expect(apiFetch).toHaveBeenCalledTimes(2));
  // 주소가 바뀌면 등록 칸은 다시 접힌다. 다시 펼쳐도 이전 대상의 동의는 없다
  expect(screen.queryByRole("checkbox")).toBeNull();
  fireEvent.click(screen.getByRole("button", { name: "대상 등록하기" }));
  expect(screen.getByRole("checkbox")).not.toBeChecked();
  fireEvent.change(screen.getByLabelText("진단 권한 · 비변경 근거"), {
    target: { value: "승인된 테스트 소유 환경의 읽기 전용 GET 경로입니다." },
  });
  fireEvent.click(screen.getByRole("button", { name: "대상 등록 · 소유 확인 준비" }));
  await waitFor(() => expect(apiFetch).toHaveBeenCalledTimes(3));
  expect(JSON.parse(String(vi.mocked(apiFetch).mock.calls[2][1]?.body)).methods).toEqual(["GET", "HEAD"]);
});

it("시안 v91 모양: 소유 확인 필요 딱지 · 등록 칸은 접혀 있다가 펼치면 첫 칸으로", async () => {
  vi.mocked(apiFetch).mockResolvedValueOnce({});
  render(<TargetApprovalPanel url={url} disabled={false} />);
  await waitFor(() => expect(apiFetch).toHaveBeenCalledTimes(1));
  expect(screen.getByText("소유 확인 필요")).toBeVisible();
  expect(screen.getByText(/아직 승인되지 않은 대상입니다/)).toHaveTextContent("owned.example.test");
  // 접혀 있을 때는 입력칸이 없다 (시안처럼 한 줄 + 버튼)
  expect(screen.queryByLabelText("진단 권한 · 비변경 근거")).toBeNull();
  fireEvent.click(screen.getByRole("button", { name: "대상 등록하기" }));
  await waitFor(() => expect(screen.getByLabelText("허용할 읽기 경로 · 한 줄에 하나")).toHaveFocus());
  fireEvent.click(screen.getByRole("button", { name: "접기" }));
  expect(screen.queryByLabelText("진단 권한 · 비변경 근거")).toBeNull();
  expect(apiFetch).toHaveBeenCalledTimes(1);
});
it("시안 v91 모양: 소유 확인 대기는 TXT 레코드 이름 · 유형 · 값과 복사 버튼", async () => {
  const writeText = vi.fn().mockResolvedValue(undefined);
  // 시험 뒤 원래 클립보드로 되돌린다 (다른 시험에 새지 않게)
  const before = Object.getOwnPropertyDescriptor(navigator, "clipboard");
  Object.defineProperty(navigator, "clipboard", { value: { writeText }, configurable: true });
  onTestFinished(() => {
    if (before) Object.defineProperty(navigator, "clipboard", before);
    else Reflect.deleteProperty(navigator, "clipboard");
  });
  vi.mocked(apiFetch).mockResolvedValueOnce({ target: pending });
  render(<TargetApprovalPanel url={url} disabled={false} />);
  await screen.findByRole("button", { name: "TXT 레코드 확인" });
  expect(screen.getByText("소유 확인 필요")).toBeVisible();
  expect(document.getElementById("targetChallengeName")).toHaveTextContent(pending.challenge_name);
  expect(document.getElementById("targetChallengeValue")).toHaveTextContent(pending.challenge_value);
  expect(screen.getByText("TXT")).toBeVisible();
  fireEvent.click(screen.getByRole("button", { name: "값 복사" }));
  expect(writeText).toHaveBeenCalledWith(pending.challenge_value);
  await screen.findByText("복사됨");
  // 복사는 서버에 아무것도 보내지 않는다
  expect(apiFetch).toHaveBeenCalledTimes(1);
});
