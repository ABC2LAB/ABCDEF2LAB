import "@testing-library/jest-dom/vitest";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, onTestFinished, vi } from "vitest";

vi.mock("@/lib/diagnosis/api", async (original) => ({
  ...(await original<typeof import("@/lib/diagnosis/api")>()),
  apiFetch: vi.fn(),
}));

import { ApiError, apiFetch } from "@/lib/diagnosis/api";

import { dateTimeOf } from "../live/map";
import {
  PLAIN_TARGET_WARNING,
  TARGET_APPROVAL_HOURS,
  TARGET_READONLY_STATEMENT,
  TargetApprovalPanel,
} from "../views/live/TargetApprovalPanel";

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
/** 점검 근거 진술 체크(글 칸 대신, 사용자 11/24) */
const attest = () => screen.getByRole("checkbox", { name: /점검 권한이 있고/ });
beforeEach(() => vi.resetAllMocks());
afterEach(cleanup);
it.each([
  "ftp://owned.example.test/read",
  "http://user:password@owned.example.test/read",
  "https://user:password@owned.example.test/read",
])("불가한 origin은 조회·등록하지 않는다: %s", (url) => {
  render(<TargetApprovalPanel requestLimit={4} url={url} disabled={false} />);
  // 정본 http·https origin 이 아니면 대상 승인 칸을 아예 그리지 않는다 (등록 버튼 없음). http 대상은 받는다(C22).
  expect(screen.queryByRole("button", { name: "대상 등록" })).toBeNull();
  expect(screen.queryByRole("button", { name: "대상 등록하기" })).toBeNull();
  expect(apiFetch).not.toHaveBeenCalled();
});
// http 대상(C22)도 등록할 수 있고, 승인 응답에는 TLS 고정값이 없다. 고정값이 붙은 http 승인 응답은 받지 않는다.
it("http 대상도 등록 칸을 그리고 TLS 고정값 없는 승인을 받는다", async () => {
  const plainURL = "http://owned.example.test/read";
  const plain = { ...approved, origin: "http://owned.example.test", port: 80, tls_spki_sha256: undefined };
  vi.mocked(apiFetch).mockResolvedValueOnce({ target: plain });
  render(<TargetApprovalPanel requestLimit={4} url={plainURL} disabled={false} />);
  await screen.findByText(/서버가 확인한 연결 .*TLS 없음\(http\)/);
  // http 대상은 평문 고지를 보인다(#527 이경준 리뷰)
  expect(screen.getByText(PLAIN_TARGET_WARNING)).toBeInTheDocument();
  cleanup();
  vi.mocked(apiFetch).mockResolvedValueOnce({ target: { ...plain, tls_spki_sha256: "a".repeat(64) } });
  render(<TargetApprovalPanel requestLimit={4} url={plainURL} disabled={false} />);
  await waitFor(() => expect(apiFetch).toHaveBeenCalledTimes(2));
  expect(screen.queryByText(/서버가 확인한 연결/)).toBeNull();
});
it("https 승인 대상에는 평문 고지가 없고, http 등록 칸에는 있다", async () => {
  vi.mocked(apiFetch).mockResolvedValueOnce({ target: approved });
  render(<TargetApprovalPanel requestLimit={4} url={url} disabled={false} />);
  await screen.findByText(/서버가 확인한 연결 .*TLS 공개키/);
  expect(screen.queryByText(PLAIN_TARGET_WARNING)).toBeNull();
  cleanup();
  vi.mocked(apiFetch).mockResolvedValueOnce({});
  render(<TargetApprovalPanel requestLimit={4} url="http://owned.example.test/read" disabled={false} />);
  await waitFor(() => expect(apiFetch).toHaveBeenCalledTimes(2));
  fireEvent.click(screen.getByRole("button", { name: "대상 등록하기" }));
  expect(screen.getByText(PLAIN_TARGET_WARNING)).toBeInTheDocument();
});
it.each([404, 501])("API %s는 패널을 숨긴다", async (status) => {
  vi.mocked(apiFetch).mockRejectedValue(new ApiError(status, "unsupported"));
  render(<TargetApprovalPanel requestLimit={4} url={url} disabled={false} />);
  await waitFor(() => expect(screen.queryByText("대상 권한 · 운영자 승인")).toBeNull());
  expect(apiFetch).toHaveBeenCalledTimes(1);
});
it("등록 화면에 GET/HEAD · query 무해성을 서버가 검증하지 않는다는 고지가 보인다", async () => {
  vi.mocked(apiFetch).mockResolvedValueOnce({});
  render(<TargetApprovalPanel requestLimit={4} url={url} disabled={false} />);
  await waitFor(() => expect(apiFetch).toHaveBeenCalledTimes(1));
  fireEvent.click(screen.getByRole("button", { name: "대상 등록하기" }));
  // 진술 체크 한 줄은 늘 보이고, 무해성 미검증 고지 등 원래 고지 문장은 (?) 메모를 열면 보인다(사용자 11/18 · 11/24)
  const note = document.getElementById("targetHarmlessNote") as HTMLElement;
  expect(note).toHaveTextContent(TARGET_READONLY_STATEMENT);
  expect(note).not.toHaveTextContent("서버는 GET · HEAD · query 의 무해성을 검증하지 않음");
  expect(screen.getByText(/서버는 GET\/HEAD 의 무해성을 검증하지 않습니다/)).not.toBeVisible();
  fireEvent.click(screen.getByRole("button", { name: "읽기 승인 설명 보기" }));
  expect(screen.getByText(/서버는 GET\/HEAD 의 무해성을 검증하지 않습니다/)).toBeVisible();
  expect(screen.getByText(/서버는 query 의 무해성을 검증하지 않으니/)).toBeVisible();
  expect(screen.getByText(/상태 변경 범위는 별도 동의한 설치에서만 등록할 수 있습니다/)).toBeVisible();
});
it("등록의 읽기 범위와 TLS 사전 접촉을 안내한다", async () => {
  vi.mocked(apiFetch).mockResolvedValueOnce({}).mockResolvedValueOnce({ target: pending });
  render(<TargetApprovalPanel requestLimit={4} url={url} disabled={false} />);
  await waitFor(() => expect(apiFetch).toHaveBeenCalledTimes(1));
  fireEvent.click(screen.getByRole("button", { name: "대상 등록하기" }));
  fireEvent.click(screen.getByRole("button", { name: "읽기 승인 설명 보기" }));
  expect(screen.getByText(/후보 IP별 연결\(https 대상은 TLS 확인\)/)).toBeVisible();
  // 진술에 체크하기 전에는 등록하지 않는다
  expect(screen.getByRole("button", { name: "대상 등록" })).toBeDisabled();
  fireEvent.click(attest());
  fireEvent.click(screen.getByRole("button", { name: "대상 등록" }));
  await screen.findByRole("button", { name: "TXT 레코드 확인" });
  const [path, init, retry] = vi.mocked(apiFetch).mock.calls[1];
  expect(path).toBe("/api/product/targets");
  expect(init?.method).toBe("POST");
  expect(retry).toBe(false);
  expect(JSON.parse(String(init?.body))).toEqual({
    url,
    path_prefixes: ["/read"],
    methods: ["GET", "HEAD"],
    readonly_basis: TARGET_READONLY_STATEMENT,
    requests: 4,
    valid_hours: 4,
  });
  expect(screen.queryByText(/승인된 대상:/)).toBeNull();
});
it("경로 · 점검 근거 글 · 유효 시간 칸 없이, 주소의 경로를 계약 기본 4시간으로 등록한다 (사용자 11/24)", async () => {
  // 서버 상한(diagnosisprep.MaxTargetApprovalHours 720) 안, 대상 승인 계약의 UI 기본값
  expect(TARGET_APPROVAL_HOURS).toBe(4);
  // 운영자 진술은 서버가 20자 이상만 받는다
  expect(new TextEncoder().encode(TARGET_READONLY_STATEMENT).length).toBeGreaterThanOrEqual(20);
  const narrow = "https://owned.example.test/users/v1";
  vi.mocked(apiFetch).mockResolvedValueOnce({}).mockResolvedValueOnce({ target: pending });
  render(<TargetApprovalPanel requestLimit={4} url={narrow} disabled={false} />);
  await waitFor(() => expect(apiFetch).toHaveBeenCalledTimes(1));
  fireEvent.click(screen.getByRole("button", { name: "대상 등록하기" }));
  expect(screen.queryByLabelText("승인 유효 시간")).toBeNull();
  expect(screen.queryByLabelText("점검할 경로 (조회만)")).toBeNull();
  expect(screen.queryByLabelText("점검 근거")).toBeNull();
  // 승인될 경로는 주소에서 읽어 보인다 — 좁히려면 주소를 그 경로까지 넣는다
  expect(document.getElementById("targetScopeLine")).toHaveTextContent("승인 경로 /users/v1 아래");
  fireEvent.click(attest());
  fireEvent.click(screen.getByRole("button", { name: "대상 등록" }));
  await screen.findByRole("button", { name: "TXT 레코드 확인" });
  const body = JSON.parse(String(vi.mocked(apiFetch).mock.calls[1][1]?.body));
  expect(body).toMatchObject({
    path_prefixes: ["/users/v1"],
    methods: ["GET", "HEAD"],
    readonly_basis: TARGET_READONLY_STATEMENT,
    requests: 4,
    valid_hours: 4,
  });
});
it("승인된 대상의 만료는 읽을 수 있는 현지 시각으로 보인다", async () => {
  vi.mocked(apiFetch).mockResolvedValueOnce({ target: approved });
  render(<TargetApprovalPanel requestLimit={4} url={url} disabled={false} />);
  await screen.findByText(new RegExp(`만료 ${dateTimeOf(approved.expires_at)}`));
  expect(screen.queryByText(/2099-01-01T00:00:00Z/)).toBeNull();
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
  render(<TargetApprovalPanel requestLimit={4} url={url} disabled={false} onChanged={changed} />);
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
  render(<TargetApprovalPanel requestLimit={4} url={url} disabled={false} onChanged={changed} />);
  fireEvent.click(await screen.findByRole("button", { name: "승인 철회" }));
  await screen.findByText(/승인을 철회했습니다/);
  expect(screen.queryByRole("button", { name: "승인 철회" })).toBeNull();
  expect(screen.queryByText(/승인된 대상:/)).toBeNull();
  fireEvent.click(screen.getByRole("button", { name: "대상 등록하기" }));
  expect(screen.getByRole("button", { name: "대상 등록" })).toBeVisible();
  expect(changed).toHaveBeenCalledTimes(1);
});
it("다른 origin의 승인 응답은 적용하지 않는다", async () => {
  const changed = vi.fn();
  vi.mocked(apiFetch)
    .mockResolvedValueOnce({ target: pending })
    .mockResolvedValueOnce({ target: { ...approved, origin: "https://other.example.test" } });
  render(<TargetApprovalPanel requestLimit={4} url={url} disabled={false} onChanged={changed} />);
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
  const view = render(<TargetApprovalPanel requestLimit={4} url={url} disabled={false} onChanged={changed} />);
  const allow = await screen.findByRole("button", { name: "TXT 레코드 확인" });
  fireEvent.click(allow);
  fireEvent.click(allow);
  expect(apiFetch).toHaveBeenCalledTimes(2);
  view.rerender(
    <TargetApprovalPanel requestLimit={4} url="https://new.example.test/read" disabled={false} onChanged={changed} />,
  );
  await waitFor(() => expect(apiFetch).toHaveBeenCalledTimes(3));
  await act(async () => finish({ target: approved }));
  expect(changed).not.toHaveBeenCalled();
  expect(screen.queryByText(/승인된 대상:/)).toBeNull();
});

it("새 origin에는 이전 대상의 쓰기 동의가 넘어가지 않는다", async () => {
  vi.mocked(apiFetch).mockResolvedValue({});
  const view = render(<TargetApprovalPanel requestLimit={4} url={url} disabled={false} allowStateChanges />);
  await waitFor(() => expect(apiFetch).toHaveBeenCalledTimes(1));
  fireEvent.click(screen.getByRole("button", { name: "대상 등록하기" }));
  const write = () => screen.getByRole("checkbox", { name: /복구 가능한 소유 시험환경/ });
  // 쓰기 동의는 AI 자동 모드가 건별 확인 없이 보낸다는 뜻까지 보인다(C21, #525 이경준 리뷰)
  expect(write().closest("label")).toHaveTextContent(/AI 자동 모드는 건별 확인 없이 보냄/);
  // 실제 집행대로 적는다: DELETE만 보내지 않고, 초기화·결제 같은 경로는 운영자가 제외 경로에 넣어야 막힌다(2026-11-21,
  // 서버 동의 판 auto_without_confirmation_v2). 이전 문구 "삭제·초기화·결제는 제외"는 지키지 못하는 약속이었다.
  expect(write().closest("label")).toHaveTextContent(/DELETE 요청은 보내지 않음/);
  expect(write().closest("label")).toHaveTextContent(/제외 경로에\s*넣어야 막힘/);
  expect(write().closest("label")).not.toHaveTextContent(/삭제·초기화·결제는 제외/);
  expect(write().closest("label")).toHaveTextContent(
    "어떤 쓰기 요청이 나갈지 미리 모두 알 수 없고 대상 데이터나 상태가 바뀔 수 있음",
  );
  fireEvent.click(write());
  expect(write()).toBeChecked();
  view.rerender(
    <TargetApprovalPanel requestLimit={4} url="https://second.example.test/read" disabled={false} allowStateChanges />,
  );
  await waitFor(() => expect(apiFetch).toHaveBeenCalledTimes(2));
  // 주소가 바뀌면 등록 칸은 다시 접힌다. 다시 펼쳐도 이전 대상의 동의는 없다
  expect(screen.queryByRole("checkbox")).toBeNull();
  fireEvent.click(screen.getByRole("button", { name: "대상 등록하기" }));
  expect(write()).not.toBeChecked();
  fireEvent.click(attest());
  fireEvent.click(screen.getByRole("button", { name: "대상 등록" }));
  await waitFor(() => expect(apiFetch).toHaveBeenCalledTimes(3));
  expect(JSON.parse(String(vi.mocked(apiFetch).mock.calls[2][1]?.body)).methods).toEqual(["GET", "HEAD"]);
});

it("시안 v91 모양: 소유 확인 필요 딱지 · 등록 칸은 접혀 있다가 펼치면 첫 칸으로", async () => {
  vi.mocked(apiFetch).mockResolvedValueOnce({});
  render(<TargetApprovalPanel requestLimit={4} url={url} disabled={false} />);
  await waitFor(() => expect(apiFetch).toHaveBeenCalledTimes(1));
  expect(screen.getByText("소유 확인 필요")).toBeVisible();
  expect(screen.getByText(/아직 승인되지 않은 대상입니다/)).toHaveTextContent("owned.example.test");
  // 접혀 있을 때는 입력칸이 없다 (시안처럼 한 줄 + 버튼)
  expect(screen.queryByRole("checkbox", { name: /점검 권한이 있고/ })).toBeNull();
  fireEvent.click(screen.getByRole("button", { name: "대상 등록하기" }));
  await waitFor(() => expect(attest()).toHaveFocus());
  fireEvent.click(screen.getByRole("button", { name: "접기" }));
  expect(screen.queryByRole("checkbox", { name: /점검 권한이 있고/ })).toBeNull();
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
  render(<TargetApprovalPanel requestLimit={4} url={url} disabled={false} />);
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

// 회차당 요청 상한은 설치가 프로필로 준 값만 쓴다. 고정값으로 등록하지 않고, 값이 없으면 등록을 막는다.
it("설치 상한이 없으면 등록하지 않고 이유를 보인다", async () => {
  vi.mocked(apiFetch).mockResolvedValueOnce({});
  render(<TargetApprovalPanel url={url} disabled={false} />);
  await waitFor(() => expect(apiFetch).toHaveBeenCalledTimes(1));
  fireEvent.click(screen.getByRole("button", { name: "대상 등록하기" }));
  fireEvent.click(attest());
  expect(screen.getByText("설치의 회차당 요청 상한을 받지 못해 대상을 등록할 수 없습니다")).toBeInTheDocument();
  expect(screen.getByRole("button", { name: "대상 등록" })).toBeDisabled();
  expect(apiFetch).toHaveBeenCalledTimes(1);
});
it("설치 상한으로 등록하고 등록·승인 화면에 회차당 요청 상한을 보인다", async () => {
  vi.mocked(apiFetch)
    .mockResolvedValueOnce({})
    .mockResolvedValueOnce({ target: { ...pending, requests: 400 } });
  render(<TargetApprovalPanel url={url} disabled={false} requestLimit={400} />);
  await waitFor(() => expect(apiFetch).toHaveBeenCalledTimes(1));
  fireEvent.click(screen.getByRole("button", { name: "대상 등록하기" }));
  // 등록 칸엔 상한 줄을 두지 않는다(사용자 11/18) — 값은 설치 상한 그대로 보낸다
  expect(document.getElementById("targetRequestLimit")).toBeNull();
  fireEvent.click(attest());
  fireEvent.click(screen.getByRole("button", { name: "대상 등록" }));
  await screen.findByRole("button", { name: "TXT 레코드 확인" });
  expect(JSON.parse(String(vi.mocked(apiFetch).mock.calls[1][1]?.body)).requests).toBe(400);
  expect(document.getElementById("targetPendingRequestLimit")).toHaveTextContent("회차당 요청 상한 400");
  cleanup();
  vi.mocked(apiFetch).mockResolvedValueOnce({ target: { ...approved, requests: 400 } });
  render(<TargetApprovalPanel url={url} disabled={false} requestLimit={400} />);
  await screen.findByText(/회차당 요청 상한 400 · 만료/);
});

// 새 점검 주소 옆 인증 칸(사용자 11/24): 상자는 머리 딱지 · 접힌 상자 없이 열리고, 상태는 onStatus 로 부모에 알린다
describe("주소 옆 인증 칸 모드(openSignal · onStatus)", () => {
  it("등록 전에는 상자 없이 'none' 을 알리고, 신호가 오르면 등록 칸을 펼친다", async () => {
    vi.mocked(apiFetch).mockResolvedValueOnce({});
    const onStatus = vi.fn();
    const { rerender } = render(
      <TargetApprovalPanel requestLimit={4} url={url} disabled={false} openSignal={0} onStatus={onStatus} />,
    );
    await waitFor(() => expect(onStatus).toHaveBeenLastCalledWith("none"));
    // 서버 답 전에는 '등록 안 됨'이 아니라 '묻는 중'이었다
    expect(onStatus.mock.calls[0][0]).toBe("loading");
    expect(screen.queryByRole("button", { name: "대상 등록하기" })).toBeNull();
    expect(screen.queryByText("소유 확인 필요")).toBeNull();
    expect(document.getElementById("targetApproval")).toBeNull();
    rerender(<TargetApprovalPanel requestLimit={4} url={url} disabled={false} openSignal={1} onStatus={onStatus} />);
    expect(await screen.findByRole("checkbox", { name: /점검 권한이 있고/ })).toBeInTheDocument();
    expect(attest()).toHaveFocus();
    expect(screen.queryByText("소유 확인 필요")).toBeNull();
    // 접기 버튼 없이 [URL 인증]을 다시 누르면(신호가 또 오르면) 닫힌다(사용자 11/24)
    expect(screen.queryByRole("button", { name: "접기" })).toBeNull();
    rerender(<TargetApprovalPanel requestLimit={4} url={url} disabled={false} openSignal={2} onStatus={onStatus} />);
    expect(screen.queryByRole("checkbox", { name: /점검 권한이 있고/ })).toBeNull();
    rerender(<TargetApprovalPanel requestLimit={4} url={url} disabled={false} openSignal={3} onStatus={onStatus} />);
    expect(await screen.findByRole("checkbox", { name: /점검 권한이 있고/ })).toBeInTheDocument();
  });

  it("등록 뒤 TXT 대기 · 승인 상태를 알리고, 상자 안 딱지는 두지 않는다", async () => {
    vi.mocked(apiFetch).mockResolvedValueOnce({ target: pending });
    const onStatus = vi.fn();
    render(<TargetApprovalPanel requestLimit={4} url={url} disabled={false} openSignal={0} onStatus={onStatus} />);
    await waitFor(() => expect(onStatus).toHaveBeenLastCalledWith("pending"));
    expect(screen.getByRole("button", { name: "TXT 레코드 확인" })).toBeInTheDocument();
    expect(screen.queryByText("소유 확인 필요")).toBeNull();
    cleanup();
    vi.mocked(apiFetch).mockResolvedValueOnce({ target: approved });
    render(<TargetApprovalPanel requestLimit={4} url={url} disabled={false} openSignal={0} onStatus={onStatus} />);
    await waitFor(() => expect(onStatus).toHaveBeenLastCalledWith("approved"));
    expect(screen.getByText(/서버가 확인한 연결/)).toBeInTheDocument();
    expect(screen.queryByText("승인된 대상")).toBeNull();
  });

  it("확인 실패 · API 없음 · 쓸 수 없는 주소를 '등록 안 됨'과 나눠 알린다", async () => {
    const onStatus = vi.fn();
    vi.mocked(apiFetch).mockRejectedValueOnce(new Error("network"));
    render(<TargetApprovalPanel requestLimit={4} url={url} disabled={false} openSignal={0} onStatus={onStatus} />);
    await waitFor(() => expect(onStatus).toHaveBeenLastCalledWith("uncertain"));
    // 접혀 있어도 다시 조회는 보인다
    expect(screen.getByRole("button", { name: "대상 승인 다시 조회" })).toBeInTheDocument();
    cleanup();
    vi.mocked(apiFetch).mockReset();
    vi.mocked(apiFetch).mockRejectedValueOnce(new ApiError(404, "unsupported"));
    render(<TargetApprovalPanel requestLimit={4} url={url} disabled={false} openSignal={0} onStatus={onStatus} />);
    await waitFor(() => expect(onStatus).toHaveBeenLastCalledWith("unsupported"));
    cleanup();
    render(
      <TargetApprovalPanel requestLimit={4} url="ftp://x.test/" disabled={false} openSignal={0} onStatus={onStatus} />,
    );
    await waitFor(() => expect(onStatus).toHaveBeenLastCalledWith("invalid"));
  });

  it("확인 실패여도 [URL 인증]으로 등록 칸을 열고 한 번 더 묻는다 · 확인될 때까지 등록은 꺼 두고 '승인 안 됨'으로 단정하지 않는다", async () => {
    vi.mocked(apiFetch).mockRejectedValue(new ApiError(503, "mirror_offline_not_saved"));
    const onStatus = vi.fn();
    const { rerender } = render(
      <TargetApprovalPanel requestLimit={4} url={url} disabled={false} openSignal={0} onStatus={onStatus} />,
    );
    await waitFor(() => expect(onStatus).toHaveBeenLastCalledWith("uncertain"));
    rerender(<TargetApprovalPanel requestLimit={4} url={url} disabled={false} openSignal={1} onStatus={onStatus} />);
    expect(await screen.findByRole("checkbox", { name: /점검 권한이 있고/ })).toBeDisabled();
    await waitFor(() => expect(apiFetch).toHaveBeenCalledTimes(2));
    expect(screen.getByRole("button", { name: "대상 등록" })).toBeDisabled();
    expect(screen.getByText(/승인 상태를 확인하지 못했습니다/)).toBeInTheDocument();
    expect(screen.queryByText(/아직 승인되지 않은 대상입니다/)).toBeNull();
    // 다시 물어 답을 받으면 등록할 수 있다
    vi.mocked(apiFetch).mockResolvedValueOnce({});
    fireEvent.click(screen.getByRole("button", { name: "대상 승인 다시 조회" }));
    await waitFor(() => expect(onStatus).toHaveBeenLastCalledWith("none"));
    expect(attest()).toBeEnabled();
  });
});
