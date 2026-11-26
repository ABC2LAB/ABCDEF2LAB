import "@testing-library/jest-dom/vitest";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

vi.mock("@/lib/diagnosis/api", () => ({ apiFetch: vi.fn() }));

import { apiFetch } from "@/lib/diagnosis/api";

import { type Shell, ShellContext, type View } from "../shell";
import { useWb } from "../store";
import { TargetAccountsLive } from "../views/live/TargetAccountsLive";

/** 시험 계정 화면은 보일 때(경로 classify) 대상 목록을 읽는다(F2). 시험은 그 경로로 그린다. */
function withRoute(route: View, goTo: (v: View) => void = () => undefined) {
  const shell = {
    route,
    navSeq: 1,
    narrow: false,
    phone: false,
    reduce: false,
    inspOpen: false,
    railOpen: false,
    railNewAtRoute: false,
    goTo,
    openInsp: () => undefined,
    closeInsp: () => undefined,
    openRail: () => undefined,
    closeRail: () => undefined,
    foldRail: () => undefined,
    dashPick: () => undefined,
  } as Shell;
  return (
    <ShellContext.Provider value={shell}>
      <TargetAccountsLive />
    </ShellContext.Provider>
  );
}

const api = vi.mocked(apiFetch);

/** 경로·메서드별 응답. GET=대상/계정 목록, POST · PUT=쓰기. 쓰기 뒤 뷰는 GET 으로 다시 읽는다. */
// 서버는 아직 classification · account_statement 를 싣는다(표시 · 해제 API를 서버에서 걷기 전) — 화면은 읽지 않는다
function serve() {
  api.mockImplementation((path: string, init?: RequestInit) => {
    const method = init?.method ?? "GET";
    if (path === "/api/product/targets")
      return Promise.resolve({ targets: [{ id: "t1", origin: "https://a.example", state: "approved" }] }) as never;
    if (path === "/api/product/targets/t1/accounts" && method === "GET")
      return Promise.resolve({
        accounts: [
          { label: "owner", classification: "test", classified_by: "op-1" },
          { label: "peer", classification: "test", classified_by: "op-1" },
          { label: "legacy", classification: "real" },
        ],
        account_statement: "소유·승인된 일회용 시험 계정이라는 진술",
      }) as never;
    if (path === "/api/product/targets/t1/login" && method === "GET") return Promise.resolve({}) as never;
    if (method === "POST") return Promise.resolve({}) as never; // 등록
    if (method === "PUT" && path.endsWith("/credential"))
      return Promise.resolve({ account: { credential_configured: true } }) as never;
    return Promise.reject(new Error(`unexpected ${method} ${path}`)) as never;
  });
}

afterEach(() => {
  cleanup();
  api.mockReset();
});

async function pickTarget() {
  fireEvent.change(await screen.findByLabelText("계정을 관리할 대상"), { target: { value: "t1" } });
  await screen.findByLabelText("계정 목록");
}

describe("TargetAccountsLive (실 API 읽기·쓰기)", () => {
  it("새 점검에서 오면 그 대상을 미리 고르고, '새 점검으로'로 돌아간다(사용자 11/23)", async () => {
    serve();
    useWb.setState({ focusTarget: { id: "t1", seq: 1 } });
    const goTo = vi.fn();
    render(withRoute("classify", goTo));
    expect(await screen.findByLabelText("계정 목록")).toBeInTheDocument();
    expect(screen.getByLabelText("계정을 관리할 대상")).toHaveValue("t1");
    fireEvent.click(screen.getByRole("button", { name: "새 점검으로 돌아가기" }));
    expect(goTo).toHaveBeenCalledWith("new");
    useWb.setState({ focusTarget: null });
  });

  // 계정 분류 폐지(사용자 결정 2026-11-26): 분류 딱지 · 시험 표시 · 표시 해제 · 표시 진술이 없다
  it("시험 계정만 등록하라는 안내 + 대상 선택 후 계정 목록을 분류 없이 그린다", async () => {
    serve();
    render(withRoute("classify"));
    expect(screen.getByText(/대상에서 쓸 시험 계정만 등록하세요/)).toBeInTheDocument();
    await pickTarget();
    // 시험 계정 A · B(owner · peer)는 위 로그인 상자, 그 밖의 계정(legacy)은 아래 '다른 계정' 목록(사용자 11/24).
    // A · B 는 등록 순서대로 고르고 분류(real)와 상관없다
    const box = within(screen.getByRole("region", { name: "시험 계정" }));
    await waitFor(() =>
      expect(within(box.getByRole("group", { name: "계정 A" })).getByText("계정 A")).toHaveAttribute(
        "title",
        "별칭 owner",
      ),
    );
    const list = within(screen.getByLabelText("계정 목록"));
    expect(list.getByText("legacy")).toBeInTheDocument();
    expect(list.queryByText("owner")).toBeNull();
    expect(list.queryByText("peer")).toBeNull();
    expect(document.body).not.toHaveTextContent(/실계정 취급|시험 표시|표시 해제|미표시|진술/);
    expect(screen.queryByRole("checkbox")).toBeNull();
    expect(screen.queryByRole("button", { name: /시험 표시|표시 해제|동의하고 표시/ })).toBeNull();
  });

  it("§2: 내보내기·공유·export 버튼을 두지 않는다", async () => {
    serve();
    render(withRoute("classify"));
    await pickTarget();
    for (const el of [...screen.queryAllByRole("button"), ...screen.queryAllByRole("link")]) {
      expect(el.textContent ?? "").not.toMatch(/내보내기|공유|export/i);
    }
  });

  it("승인된 시험 계정도 '모델 입력 가능'으로 보이지 않는다(권한은 별도)", async () => {
    serve();
    render(withRoute("classify"));
    await pickTarget();
    const list = within(screen.getByLabelText("계정 목록"));
    expect(list.queryByText(/모델 입력 가능/)).toBeNull();
    // 줄 아래 작은 글(주소 · '권한은 별도' · 표시자)은 뺐다(사용자 11/24 '로그인 창처럼') — 그래도 권한을 주장하는 말은 없다
    expect(document.body).not.toHaveTextContent(/모델 입력 가능/);
  });

  // 사용자 11/24: 라벨을 적어 등록하던 칸 대신 맨 위 시험 계정 A · B 로그인 상자(이름 고정)에서 등록 → 자격을 한 번에
  it("시험 계정 A · B 상자: 이름은 고정이고, 빈 줄(B)을 채워 저장하면 등록 → 자격 순서로 보내고 시험 표시는 보내지 않는다", async () => {
    serve();
    render(withRoute("classify"));
    await pickTarget();
    expect(screen.queryByLabelText("새 계정 라벨")).toBeNull();
    const box = within(screen.getByRole("region", { name: "시험 계정" }));
    // 선언을 못 읽으면 이미 있는 등록 계정(owner)을 A 로, B 는 그다음 등록 계정(peer)
    await waitFor(() =>
      expect(within(box.getByRole("group", { name: "계정 A" })).getByText("계정 A")).toHaveAttribute(
        "title",
        "별칭 owner",
      ),
    );
    expect(within(box.getByRole("group", { name: "계정 B" })).getByText("계정 B")).toHaveAttribute(
      "title",
      "별칭 peer",
    );
    fireEvent.change(box.getByLabelText("계정 B 아이디"), { target: { value: "u-b" } });
    fireEvent.change(box.getByLabelText("계정 B 비밀번호"), { target: { value: "synthetic-pass-b-1234" } });
    fireEvent.click(box.getByRole("button", { name: "저장" }));
    await waitFor(() =>
      expect(api).toHaveBeenCalledWith(
        "/api/product/targets/t1/accounts/peer/credential",
        expect.objectContaining({ method: "PUT" }),
      ),
    );
    // 이미 등록된 계정이라 등록은 건너뛰고, 시험 표시는 어디에도 없다
    expect(api).not.toHaveBeenCalledWith(
      "/api/product/targets/t1/accounts",
      expect.objectContaining({ method: "POST" }),
    );
    expect(api).not.toHaveBeenCalledWith(expect.stringContaining("/mark-test"), expect.anything());
    expect(box.getByLabelText("계정 B 비밀번호")).toHaveValue("");
    expect(await box.findByText("저장함")).toBeInTheDocument();
  });

  // #972 이동찬 P2: 서버 목록은 라벨순이다. 선언이 없으면 A · B 는 등록 순서라, 뒤에 등록한 라벨 앞 계정이 A 를 빼앗지 않는다
  it("선언이 없으면 A · B 는 등록 시각 순 — 라벨순 서버 목록에서 뒤에 등록한 tester-a 가 A 를 빼앗지 않는다", async () => {
    api.mockImplementation((path: string, init?: RequestInit) => {
      const method = init?.method ?? "GET";
      if (path === "/api/product/targets")
        return Promise.resolve({ targets: [{ id: "t1", origin: "https://a.example", state: "approved" }] }) as never;
      if (path === "/api/product/targets/t1/accounts" && method === "GET")
        return Promise.resolve({
          // 서버 listAccounts 처럼 라벨 오름차순. tester-z 를 먼저 등록했다
          accounts: [
            { label: "tester-a", registered_at: "2026-11-26T10:05:00Z" },
            { label: "tester-m", registered_at: "2026-11-26T10:10:00Z" },
            { label: "tester-z", registered_at: "2026-11-26T10:00:00Z" },
          ],
        }) as never;
      if (path === "/api/product/targets/t1/login" && method === "GET") return Promise.resolve({}) as never;
      if (method === "PUT" && path.endsWith("/credential"))
        return Promise.resolve({ account: { credential_configured: true } }) as never;
      return Promise.reject(new Error(`unexpected ${method} ${path}`)) as never;
    });
    render(withRoute("classify"));
    fireEvent.change(await screen.findByLabelText("계정을 관리할 대상"), { target: { value: "t1" } });
    const box = within(await screen.findByRole("region", { name: "시험 계정" }));
    await waitFor(() =>
      expect(within(box.getByRole("group", { name: "계정 A" })).getByText("계정 A")).toHaveAttribute(
        "title",
        "별칭 tester-z",
      ),
    );
    expect(within(box.getByRole("group", { name: "계정 B" })).getByText("계정 B")).toHaveAttribute(
      "title",
      "별칭 tester-a",
    );
    // '계정 A'에 넣은 자격은 먼저 등록한 tester-z 로 간다
    fireEvent.change(box.getByLabelText("계정 A 아이디"), { target: { value: "u-a" } });
    fireEvent.change(box.getByLabelText("계정 A 비밀번호"), { target: { value: "synthetic-pass-a-1234" } });
    fireEvent.click(box.getByRole("button", { name: "저장" }));
    await waitFor(() =>
      expect(api).toHaveBeenCalledWith(
        "/api/product/targets/t1/accounts/tester-z/credential",
        expect.objectContaining({ method: "PUT" }),
      ),
    );
    expect(api).not.toHaveBeenCalledWith("/api/product/targets/t1/accounts/tester-a/credential", expect.anything());
  });

  it("등록 안 된 줄(B)을 채워 저장하면 등록 뒤 자격을 보낸다", async () => {
    api.mockImplementation((path: string, init?: RequestInit) => {
      const method = init?.method ?? "GET";
      if (path === "/api/product/targets")
        return Promise.resolve({ targets: [{ id: "t1", origin: "https://a.example", state: "approved" }] }) as never;
      if (path === "/api/product/targets/t1/accounts" && method === "GET")
        return Promise.resolve({ accounts: [{ label: "owner" }] }) as never;
      if (path === "/api/product/targets/t1/login" && method === "GET") return Promise.resolve({}) as never;
      if (method === "POST") return Promise.resolve({}) as never;
      if (method === "PUT" && path.endsWith("/credential"))
        return Promise.resolve({ account: { credential_configured: true } }) as never;
      return Promise.reject(new Error(`unexpected ${method} ${path}`)) as never;
    });
    render(withRoute("classify"));
    fireEvent.change(await screen.findByLabelText("계정을 관리할 대상"), { target: { value: "t1" } });
    const box = within(await screen.findByRole("region", { name: "시험 계정" }));
    await waitFor(() =>
      expect(within(box.getByRole("group", { name: "계정 B" })).getByText("계정 B")).toHaveAttribute(
        "title",
        "별칭 tester-b",
      ),
    );
    expect(within(box.getByRole("group", { name: "계정 B" })).getByText("등록 안 됨")).toBeInTheDocument();
    fireEvent.change(box.getByLabelText("계정 B 아이디"), { target: { value: "u-b" } });
    fireEvent.change(box.getByLabelText("계정 B 비밀번호"), { target: { value: "synthetic-pass-b-1234" } });
    fireEvent.click(box.getByRole("button", { name: "저장" }));
    await waitFor(() =>
      expect(api).toHaveBeenCalledWith(
        "/api/product/targets/t1/accounts/tester-b/credential",
        expect.objectContaining({ method: "PUT" }),
      ),
    );
    const calls = api.mock.calls.map(([p, i]) => `${i?.method ?? "GET"} ${p}`);
    expect(calls.indexOf("POST /api/product/targets/t1/accounts")).toBeLessThan(
      calls.indexOf("PUT /api/product/targets/t1/accounts/tester-b/credential"),
    );
  });

  // 사용자 11/25: 11/24 B 계정 아이디 끝 공백으로 로그인이 실패했다 — 아이디 칸은 공백을 아예 받지 않는다
  it("시험 계정 A · B 아이디 칸은 공백을 넣지 못한다(타이핑 · 붙여넣기 모두, 비밀번호는 그대로)", async () => {
    serve();
    render(withRoute("classify"));
    await pickTarget();
    const box = within(screen.getByRole("region", { name: "시험 계정" }));
    const user = box.getByLabelText("계정 B 아이디") as HTMLInputElement;
    fireEvent.input(user, { target: { value: " u b\t\u3000-1 " } });
    expect(user.value).toBe("ub-1");
    const pass = box.getByLabelText("계정 B 비밀번호") as HTMLInputElement;
    fireEvent.input(pass, { target: { value: "pass word" } });
    expect(pass.value).toBe("pass word");
  });

  it("승인된 대상이 없으면 안내만 보이고 계정 목록은 없다", async () => {
    api.mockImplementation((path: string) =>
      path === "/api/product/targets"
        ? (Promise.resolve({ targets: [] }) as never)
        : (Promise.reject(new Error("x")) as never),
    );
    render(withRoute("classify"));
    expect(await screen.findByText(/승인된 대상이 없습니다/)).toBeInTheDocument();
    expect(screen.queryByLabelText("계정 목록")).toBeNull();
  });
});

// 쓰기 중 대상을 바꾸면, 늦게 온 이전 대상의 쓰기·재조회 응답(성공·실패)이 지금 대상 화면을 덮지 않는다(#538 이동찬 리뷰).
describe("TargetAccountsLive 지연 응답 귀속", () => {
  function serveTwo() {
    let settle: { resolve: () => void; reject: () => void } | null = null;
    api.mockImplementation((path: string, init?: RequestInit) => {
      const method = init?.method ?? "GET";
      if (path === "/api/product/targets")
        return Promise.resolve({
          targets: [
            { id: "t1", origin: "https://a.example", state: "approved" },
            { id: "t2", origin: "https://b.example", state: "approved" },
          ],
        }) as never;
      if (path === "/api/product/targets/t1/accounts" && method === "GET")
        return Promise.resolve({ accounts: [{ label: "account-a" }] }) as never;
      if (path === "/api/product/targets/t2/accounts" && method === "GET")
        return Promise.resolve({ accounts: [{ label: "account-b" }] }) as never;
      if (path.endsWith("/login") && method === "GET") return Promise.resolve({}) as never;
      if (path === "/api/product/targets/t1/accounts/account-a/credential" && method === "PUT")
        return new Promise((resolve, reject) => {
          settle = {
            resolve: () => resolve({ account: { credential_configured: true } }),
            reject: () => reject(new Error("boom")),
          };
        }) as never;
      return Promise.reject(new Error(`unexpected ${method} ${path}`)) as never;
    });
    return () => settle as unknown as { resolve: () => void; reject: () => void };
  }

  async function startSaveOnAThenSwitchToB() {
    render(withRoute("classify"));
    const pick = await screen.findByLabelText("계정을 관리할 대상");
    fireEvent.change(pick, { target: { value: "t1" } });
    // 시험 계정 A(account-a)의 자격 저장은 위 로그인 상자에서 한다
    const box = () => within(screen.getByRole("region", { name: "시험 계정" }));
    await waitFor(() =>
      expect(within(box().getByRole("group", { name: "계정 A" })).getByText("계정 A")).toHaveAttribute(
        "title",
        "별칭 account-a",
      ),
    );
    fireEvent.change(box().getByLabelText("계정 A 아이디"), { target: { value: "u-a" } });
    fireEvent.change(box().getByLabelText("계정 A 비밀번호"), { target: { value: "synthetic-pass-a-1234" } });
    fireEvent.click(box().getByRole("button", { name: "저장" }));
    await waitFor(() =>
      expect(api).toHaveBeenCalledWith(
        "/api/product/targets/t1/accounts/account-a/credential",
        expect.objectContaining({ method: "PUT" }),
      ),
    );
    fireEvent.change(pick, { target: { value: "t2" } });
    await waitFor(() =>
      expect(within(box().getByRole("group", { name: "계정 A" })).getByText("계정 A")).toHaveAttribute(
        "title",
        "별칭 account-b",
      ),
    );
  }

  it.each([
    "resolve",
    "reject",
  ] as const)("이전 대상 쓰기가 늦게 %s 되어도 지금 대상 목록·오류를 바꾸지 않는다", async (how) => {
    const settle = serveTwo();
    await startSaveOnAThenSwitchToB();
    await act(async () => {
      settle()[how]();
    });
    // 선택은 B, 상자도 B 그대로. A 대상의 계정 · 오류 · 저장 안내가 끼어들지 않는다.
    await waitFor(() => expect(screen.getByLabelText("계정을 관리할 대상")).toHaveValue("t2"));
    const box = screen.getByRole("region", { name: "시험 계정" });
    expect(within(within(box).getByRole("group", { name: "계정 A" })).getByText("계정 A")).toHaveAttribute(
      "title",
      "별칭 account-b",
    );
    expect(box).not.toHaveTextContent("account-a");
    expect(screen.queryByText(/작업을 처리하지 못했습니다/)).toBeNull();
    expect(screen.queryByText("저장함")).toBeNull();
  });
});

// 대상 로그인 방식(GET·PUT …/login)과 계정 자격(PUT …/credential). 자격 값은 비제어 입력에서 보낼 때 한 번 읽고 바로 비운다.
describe("TargetAccountsLive 로그인 방식 · 자격 입력", () => {
  // 합성 값(특정 대상 앱 값이 아님).
  const SAVED_LOGIN = {
    method: "POST",
    path: "/session/new",
    format: "json",
    username_field: "user_name",
    password_field: "pass_word",
    json_field: "data.access",
    header: "X-Session",
    prefix: "Token ",
  };
  const USERNAME = "synthetic-user-7f3a";
  const PASSWORD = "synthetic-pass-9c2e";

  type Opts = {
    login?: unknown;
    putLogin?: (body: unknown) => Promise<unknown>;
    putCredential?: () => Promise<unknown>;
    configuredAfterWrite?: boolean;
  };
  function serveLogin(opts: Opts = {}) {
    let credentialWritten = false;
    api.mockImplementation((path: string, init?: RequestInit) => {
      const method = init?.method ?? "GET";
      if (path === "/api/product/targets")
        return Promise.resolve({ targets: [{ id: "t1", origin: "https://a.example", state: "approved" }] }) as never;
      if (path === "/api/product/targets/t1/accounts" && method === "GET")
        return Promise.resolve({
          accounts: [
            { label: "owner", classification: "test", classified_by: "op-1", credential_configured: true },
            { label: "peer", classification: "test", classified_by: "op-1", credential_configured: true },
            {
              label: "legacy",
              classification: "real",
              ...(credentialWritten && opts.configuredAfterWrite ? { credential_configured: true } : {}),
            },
          ],
          account_statement: "진술",
        }) as never;
      if (path === "/api/product/targets/t1/login" && method === "GET")
        return Promise.resolve(opts.login === undefined ? {} : { login: opts.login }) as never;
      if (path === "/api/product/targets/t1/login" && method === "PUT")
        return (opts.putLogin ?? ((b) => Promise.resolve({ login: b })))(JSON.parse(String(init?.body))) as never;
      if (path === "/api/product/targets/t1/accounts/legacy/credential" && method === "PUT") {
        credentialWritten = true;
        return (
          opts.putCredential ?? (() => Promise.resolve({ account: { label: "legacy", credential_configured: true } }))
        )() as never;
      }
      return Promise.reject(new Error(`unexpected ${method} ${path}`)) as never;
    });
  }

  const loginSection = () => within(screen.getByRole("region", { name: "대상 로그인 방식" }));
  const row = (label: string) =>
    within(screen.getByLabelText("계정 목록")).getByText(label).closest("li") as HTMLElement;

  it("자격 입력의 사용자명 칸도 공백을 넣지 못한다(사용자 11/25)", async () => {
    serveLogin({ configuredAfterWrite: true });
    render(withRoute("classify"));
    await pickTarget();
    fireEvent.click(within(row("legacy")).getByRole("button", { name: "자격 입력" }));
    const user = within(row("legacy")).getByLabelText("사용자명") as HTMLInputElement;
    fireEvent.input(user, { target: { value: "synthetic user " } });
    expect(user.value).toBe("syntheticuser");
  });

  it("저장된 로그인 방식을 칸에 채우고, 고쳐 저장하면 PUT login(자격 값 없음)", async () => {
    serveLogin({ login: SAVED_LOGIN });
    render(withRoute("classify"));
    await pickTarget();
    expect(await screen.findByLabelText("로그인 경로 (POST)")).toHaveValue("/session/new");
    expect(screen.getByLabelText("응답 JSON 칸 (점으로 구분)")).toHaveValue("data.access");
    expect(screen.getByLabelText("헤더 값 앞붙임 (선택)")).toHaveValue("Token ");
    expect(loginSection().getByText(/저장됨 · 바꾸면 다음 점검부터/)).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("로그인 경로 (POST)"), { target: { value: "/session/start" } });
    fireEvent.click(screen.getByRole("button", { name: "로그인 방식 저장" }));
    await screen.findByText("저장했습니다.");
    const put = api.mock.calls.find(([p, i]) => p === "/api/product/targets/t1/login" && i?.method === "PUT");
    expect(JSON.parse(String(put?.[1]?.body))).toEqual({ ...SAVED_LOGIN, path: "/session/start" });
  });

  it("쿠키 방식으로 바꾸면 쿠키 이름만 보낸다", async () => {
    serveLogin({ login: SAVED_LOGIN });
    render(withRoute("classify"));
    await pickTarget();
    await screen.findByLabelText("로그인 경로 (POST)");
    fireEvent.change(screen.getByLabelText("세션 위치"), { target: { value: "cookies" } });
    fireEvent.change(screen.getByLabelText("세션 쿠키 이름 (쉼표로 구분 · 최대 8개)"), {
      target: { value: "sid, csrf" },
    });
    fireEvent.click(screen.getByRole("button", { name: "로그인 방식 저장" }));
    await screen.findByText("저장했습니다.");
    const put = api.mock.calls.find(([p, i]) => p === "/api/product/targets/t1/login" && i?.method === "PUT");
    expect(JSON.parse(String(put?.[1]?.body))).toEqual({
      method: "POST",
      path: "/session/new",
      format: "json",
      username_field: "user_name",
      password_field: "pass_word",
      cookies: ["sid", "csrf"],
    });
  });

  it("미설정이면 안내하고, 칸이 형식 밖이면 저장 버튼이 막힌다", async () => {
    serveLogin();
    render(withRoute("classify"));
    await pickTarget();
    expect(await loginSection().findByText(/저장된 로그인 방식이 없습니다/)).toBeInTheDocument();
    const save = screen.getByRole("button", { name: "로그인 방식 저장" });
    expect(save).toBeDisabled();
    fireEvent.change(screen.getByLabelText("로그인 경로 (POST)"), { target: { value: "/session/new" } });
    fireEvent.change(screen.getByLabelText("사용자명 칸 이름"), { target: { value: "user_name" } });
    fireEvent.change(screen.getByLabelText("비밀번호 칸 이름"), { target: { value: "pass_word" } });
    fireEvent.change(screen.getByLabelText("응답 JSON 칸 (점으로 구분)"), { target: { value: "data.access" } });
    expect(save).toBeEnabled();
    fireEvent.change(screen.getByLabelText("로그인 경로 (POST)"), { target: { value: "/session?x=1" } });
    expect(save).toBeDisabled();
  });

  it("저장 실패는 서버 사유를 한국어 문구로 보인다", async () => {
    serveLogin({ login: SAVED_LOGIN, putLogin: () => Promise.reject(new Error("invalid_target_login")) });
    render(withRoute("classify"));
    await pickTarget();
    await screen.findByLabelText("로그인 경로 (POST)");
    fireEvent.click(screen.getByRole("button", { name: "로그인 방식 저장" }));
    expect(await loginSection().findByRole("alert")).toHaveTextContent(/승인 경로 밖/);
  });

  it("형식 밖 login 응답은 미설정이 아니라 읽기 실패로 보인다", async () => {
    serveLogin({ login: { method: "GET", path: "/x" } });
    render(withRoute("classify"));
    await pickTarget();
    expect(await loginSection().findByText("로그인 방식을 불러오지 못했습니다.")).toBeInTheDocument();
    expect(loginSection().queryByText(/저장된 로그인 방식이 없습니다/)).toBeNull();
  });

  it("자격 등록 표시는 credential_configured 로만 보인다", async () => {
    serveLogin();
    render(withRoute("classify"));
    await pickTarget();
    // A(owner)는 위 로그인 상자 줄, legacy 는 아래 목록 줄
    const a = within(screen.getByRole("region", { name: "시험 계정" })).getByRole("group", { name: "계정 A" });
    await waitFor(() => expect(a).toHaveTextContent("자격 등록됨"));
    expect(within(row("legacy")).getByText("자격 없음")).toBeInTheDocument();
  });

  it("자격 입력: 보내는 순간 칸을 비우고, 값은 PUT 본문에만 실린다(저장소 · URL · 로그 · 화면에 없음)", async () => {
    serveLogin({ configuredAfterWrite: true });
    const setItem = vi.spyOn(Storage.prototype, "setItem");
    const logs = (["log", "info", "warn", "error", "debug"] as const).map((k) => vi.spyOn(console, k));
    const { container } = render(withRoute("classify"));
    await pickTarget();
    fireEvent.click(within(row("legacy")).getByRole("button", { name: "자격 입력" }));
    const user = within(row("legacy")).getByLabelText("사용자명") as HTMLInputElement;
    const pass = within(row("legacy")).getByLabelText("비밀번호") as HTMLInputElement;
    expect(pass).toHaveAttribute("type", "password");
    // 대상 시험 계정 자격이라 브라우저 비밀번호 관리자에 들어가지 않게 한다(#842 경준 리뷰).
    for (const el of [user, pass]) {
      expect(el).toHaveAttribute("autocomplete", "off");
      expect(el).toHaveAttribute("data-1p-ignore");
      expect(el).toHaveAttribute("data-lpignore", "true");
    }
    // form 제출이 아니다(브라우저 비밀번호 저장 제안 · 주소창 query 로 새지 않게).
    expect(pass.closest("form")).toBeNull();
    fireEvent.change(user, { target: { value: USERNAME } });
    fireEvent.change(pass, { target: { value: PASSWORD } });
    fireEvent.click(within(row("legacy")).getByRole("button", { name: "자격 등록" }));
    // 응답을 기다리기 전에 이미 비어 있다.
    expect(user.value).toBe("");
    expect(pass.value).toBe("");
    expect(api).toHaveBeenCalledWith(
      "/api/product/targets/t1/accounts/legacy/credential",
      expect.objectContaining({ method: "PUT", body: JSON.stringify({ username: USERNAME, password: PASSWORD }) }),
    );
    // 성공하면 목록을 다시 읽어 서버 표시로 바뀌고 입력 칸은 닫힌다.
    expect(await within(row("legacy")).findByText("자격 등록됨")).toBeInTheDocument();
    expect(within(row("legacy")).queryByLabelText("비밀번호")).toBeNull();
    const leaked = (v: unknown) =>
      JSON.stringify(v ?? null).includes(PASSWORD) || JSON.stringify(v ?? null).includes(USERNAME);
    expect(container.innerHTML).not.toContain(PASSWORD);
    expect(container.innerHTML).not.toContain(USERNAME);
    expect(window.location.href).not.toContain(PASSWORD);
    expect(setItem.mock.calls.some(leaked)).toBe(false);
    for (const spy of logs) expect(spy.mock.calls.some(leaked)).toBe(false);
    setItem.mockRestore();
    for (const spy of logs) spy.mockRestore();
  });

  it("6바이트보다 짧은 비밀번호는 등록 뒤 '실행 때 로그인하지 않음'을 따로 알린다(값 없음)", async () => {
    serveLogin({ configuredAfterWrite: true });
    const { container } = render(withRoute("classify"));
    await pickTarget();
    fireEvent.click(within(row("legacy")).getByRole("button", { name: "자격 입력" }));
    fireEvent.change(within(row("legacy")).getByLabelText("사용자명"), { target: { value: USERNAME } });
    fireEvent.change(within(row("legacy")).getByLabelText("비밀번호"), { target: { value: "abcde" } });
    fireEvent.click(within(row("legacy")).getByRole("button", { name: "자격 등록" }));
    expect(await screen.findByText(/legacy: 자격은 등록했지만 비밀번호가 6바이트보다 짧아/)).toBeInTheDocument();
    expect(container.innerHTML).not.toContain("abcde");
    // 6바이트면 안내하지 않는다.
    fireEvent.click(within(row("legacy")).getByRole("button", { name: "자격 다시 입력" }));
    expect(screen.queryByText(/자격은 등록했지만/)).toBeNull();
    fireEvent.change(within(row("legacy")).getByLabelText("사용자명"), { target: { value: USERNAME } });
    fireEvent.change(within(row("legacy")).getByLabelText("비밀번호"), { target: { value: "abcdef" } });
    fireEvent.click(within(row("legacy")).getByRole("button", { name: "자격 등록" }));
    await waitFor(() => expect(within(row("legacy")).queryByLabelText("비밀번호")).toBeNull());
    expect(screen.queryByText(/자격은 등록했지만/)).toBeNull();
  });

  it("등록이 실패해도 칸은 비어 있고, 서버 사유를 그 계정 줄에 한국어로 보인다", async () => {
    serveLogin({ putCredential: () => Promise.reject(new Error("account_not_found")) });
    render(withRoute("classify"));
    await pickTarget();
    fireEvent.click(within(row("legacy")).getByRole("button", { name: "자격 입력" }));
    const user = within(row("legacy")).getByLabelText("사용자명") as HTMLInputElement;
    const pass = within(row("legacy")).getByLabelText("비밀번호") as HTMLInputElement;
    fireEvent.change(user, { target: { value: USERNAME } });
    fireEvent.change(pass, { target: { value: PASSWORD } });
    fireEvent.click(within(row("legacy")).getByRole("button", { name: "자격 등록" }));
    expect(await within(row("legacy")).findByRole("alert")).toHaveTextContent(/계정을 찾지 못했습니다/);
    expect(user.value).toBe("");
    expect(pass.value).toBe("");
    expect(within(row("legacy")).getByText("자격 없음")).toBeInTheDocument();
  });

  it("형식 밖 자격(빈 비밀번호)은 보내지 않고 칸을 비운다", async () => {
    serveLogin();
    render(withRoute("classify"));
    await pickTarget();
    fireEvent.click(within(row("legacy")).getByRole("button", { name: "자격 입력" }));
    const user = within(row("legacy")).getByLabelText("사용자명") as HTMLInputElement;
    fireEvent.change(user, { target: { value: USERNAME } });
    fireEvent.click(within(row("legacy")).getByRole("button", { name: "자격 등록" }));
    expect(within(row("legacy")).getByRole("alert")).toHaveTextContent(/자격 형식이 맞지 않습니다/);
    expect(user.value).toBe("");
    expect(api).not.toHaveBeenCalledWith(expect.stringContaining("/credential"), expect.anything());
  });

  it("서버가 credential_configured 를 참으로 주지 않으면 등록됨으로 보이지 않는다", async () => {
    serveLogin({ putCredential: () => Promise.resolve({ account: { label: "legacy" } }) });
    render(withRoute("classify"));
    await pickTarget();
    fireEvent.click(within(row("legacy")).getByRole("button", { name: "자격 입력" }));
    fireEvent.change(within(row("legacy")).getByLabelText("사용자명"), { target: { value: USERNAME } });
    fireEvent.change(within(row("legacy")).getByLabelText("비밀번호"), { target: { value: PASSWORD } });
    fireEvent.click(within(row("legacy")).getByRole("button", { name: "자격 등록" }));
    expect(await within(row("legacy")).findByRole("alert")).toHaveTextContent(/자격 등록을 확인하지 못했습니다/);
    expect(within(row("legacy")).getByText("자격 없음")).toBeInTheDocument();
  });
});

describe("대상 목록 다시 읽기(사용자 2026-11-22, 1차 리허설 F2)", () => {
  it("숨긴 채 붙어 있다가 화면에 들어오면 대상 목록을 다시 읽어 그사이 승인한 대상을 보인다", async () => {
    const approvedTargets: unknown[] = [];
    api.mockImplementation((path: string) => {
      if (path === "/api/product/targets")
        return Promise.resolve({
          targets: approvedTargets,
        }) as never;
      return Promise.resolve({}) as never;
    });
    const view = render(withRoute("dashboard"));
    expect(api).not.toHaveBeenCalled(); // 숨겨진 동안은 읽지 않는다
    view.rerender(withRoute("classify"));
    await screen.findByText(/승인된 대상이 없습니다/);
    approvedTargets.push({ id: "t1", origin: "https://a.example", state: "approved" }); // 대상 등록 화면에서 승인
    view.rerender(withRoute("new"));
    view.rerender(withRoute("classify"));
    expect(await screen.findByRole("option", { name: /https:\/\/a\.example/ })).toBeInTheDocument();
  });
});
