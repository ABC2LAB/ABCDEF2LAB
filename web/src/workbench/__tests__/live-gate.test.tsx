// live/auth: 관문 상태(확인 중 · 연결 실패 · 초기 설정 필요 · 로그인 · 준비) 와 인증 끊김 알림. 팀 api 를 모의한다.

import "@testing-library/jest-dom/vitest";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const h = vi.hoisted(() => ({ listeners: new Set<() => void>() }));

vi.mock("@/lib/diagnosis/api", async (importOriginal) => {
  const real = await importOriginal<typeof import("@/lib/diagnosis/api")>();
  return {
    ...real,
    apiFetch: vi.fn(),
    readAuthStatus: vi.fn(),
    restoreSession: vi.fn(),
    signIn: vi.fn(),
    signOut: vi.fn(() => {
      for (const l of h.listeners) l();
    }),
    subscribeAuthRequired: vi.fn((l: () => void) => {
      h.listeners.add(l);
      return () => {
        h.listeners.delete(l);
      };
    }),
  };
});

import * as teamApi from "@/lib/diagnosis/api";
import { ApiError } from "@/lib/diagnosis/api";

import {
  checkLiveAuth,
  getLiveAuthState,
  LiveGate,
  setLiveAuthState,
  useLiveAuth,
  useLiveAuthBoot,
} from "../live/auth";
import { resetFeeds } from "../live/feeds";
import { useSource } from "../live/source";
import { createInitialState, useWb } from "../store";
import { Workbench } from "../Workbench";

const m = vi.mocked(teamApi);

function LogoutButton() {
  const { logout } = useLiveAuth();
  return (
    <button type="button" onClick={logout}>
      나가기
    </button>
  );
}

/** 관문 하나 + 부트 훅 (Workbench 없이). */
function Harness() {
  useLiveAuthBoot();
  return (
    <LiveGate title="대시보드">
      <p>실제 화면</p>
      <LogoutButton />
    </LiveGate>
  );
}

function online() {
  m.readAuthStatus.mockResolvedValue({ initialized: true });
  m.restoreSession.mockResolvedValue(true);
  m.apiFetch.mockResolvedValue({ active: "", tasks: [] } as never);
}

beforeEach(() => {
  h.listeners.clear();
  for (const f of [m.apiFetch, m.readAuthStatus, m.restoreSession, m.signIn]) f.mockReset();
  localStorage.clear();
  useSource.setState({ source: "live", taskId: null });
  setLiveAuthState("checking");
  resetFeeds();
  useWb.setState(createInitialState());
  window.location.hash = "";
  vi.spyOn(window, "scrollTo").mockImplementation(() => undefined);
});
afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe("LiveGate", () => {
  it("확인 중에는 머리(h1)와 안내만", () => {
    m.readAuthStatus.mockReturnValue(new Promise(() => undefined));
    render(<Harness />);
    expect(screen.getByRole("heading", { level: 1, name: "대시보드" })).toBeInTheDocument();
    expect(screen.getByRole("status")).toHaveTextContent("엔진 연결을 확인하는 중…");
    expect(screen.queryByText("실제 화면")).toBeNull();
  });

  it("연결 실패(fetch TypeError): 안내 · 다시 시도 · 예시 재생으로 보기", async () => {
    m.readAuthStatus.mockRejectedValue(new TypeError("Failed to fetch"));
    render(<Harness />);
    expect(await screen.findByText("엔진(8787)에 연결할 수 없습니다")).toBeInTheDocument();
    expect(getLiveAuthState()).toBe("offline");

    online();
    fireEvent.click(screen.getByRole("button", { name: "다시 시도" }));
    expect(await screen.findByText("실제 화면")).toBeInTheDocument();
  });

  it("개발 서버 프록시 500(본문 없음)도 연결 실패로 본다", async () => {
    m.readAuthStatus.mockRejectedValue(new ApiError(500, "HTTP 500"));
    render(<Harness />);
    expect(await screen.findByText("엔진(8787)에 연결할 수 없습니다")).toBeInTheDocument();
  });

  it("서버 오류에도 실제 연결을 유지하고 예시 전환을 표시하지 않는다", async () => {
    m.readAuthStatus.mockRejectedValue(new ApiError(0, "x"));
    render(<Harness />);
    expect(await screen.findByRole("button", { name: "다시 시도" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "예시 재생으로 보기" })).toBeNull();
    expect(useSource.getState().source).toBe("live");
  });

  it("initialized=false → 초기 설정 안내", async () => {
    m.readAuthStatus.mockResolvedValue({ initialized: false });
    render(<Harness />);
    expect(await screen.findByText("엔진 초기 설정이 필요합니다(팀 화면에서 설정)")).toBeInTheDocument();
    expect(m.restoreSession).not.toHaveBeenCalled();
  });

  it("저장된 토큰이 없으면 로그인: 실패 글 · 비밀번호 비우기 · 성공하면 화면", async () => {
    m.readAuthStatus.mockResolvedValue({ initialized: true });
    m.restoreSession.mockResolvedValue(false);
    render(<Harness />);
    const pass = await screen.findByLabelText("비밀번호");
    expect(screen.getByLabelText("아이디")).toHaveValue("ABC2LAB");

    m.signIn.mockRejectedValueOnce(new ApiError(401, "bad"));
    fireEvent.change(pass, { target: { value: "wrong-pw" } });
    fireEvent.click(screen.getByRole("button", { name: "로그인" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("아이디 또는 비밀번호가 맞지 않습니다.");
    expect(m.signIn).toHaveBeenLastCalledWith("ABC2LAB", "wrong-pw");
    expect(screen.getByLabelText("비밀번호")).toHaveValue("");

    m.signIn.mockResolvedValueOnce(undefined);
    fireEvent.change(screen.getByLabelText("비밀번호"), { target: { value: "right-pw" } });
    // 제출 즉시 비운다
    fireEvent.submit(screen.getByLabelText("비밀번호").closest("form") as HTMLFormElement);
    expect(screen.getByLabelText("비밀번호")).toHaveValue("");
    expect(await screen.findByText("실제 화면")).toBeInTheDocument();
    expect(m.signIn).toHaveBeenLastCalledWith("ABC2LAB", "right-pw");
  });

  it("토큰이 있어도 첫 호출이 401 이면 로그인", async () => {
    m.readAuthStatus.mockResolvedValue({ initialized: true });
    m.restoreSession.mockResolvedValue(true);
    m.apiFetch.mockRejectedValue(new ApiError(401, "expired"));
    render(<Harness />);
    expect(await screen.findByLabelText("비밀번호")).toBeInTheDocument();
  });

  it("첫 진단 준비 서버의 403 은 막지 않고 준비로 둔다 (섹션이 따로 접는다)", async () => {
    m.readAuthStatus.mockRejectedValue(new ApiError(403, "첫 진단"));
    m.restoreSession.mockResolvedValue(true);
    m.apiFetch.mockRejectedValue(new ApiError(403, "첫 진단"));
    render(<Harness />);
    expect(await screen.findByText("실제 화면")).toBeInTheDocument();
  });

  it("준비된 뒤 인증 끊김 알림이 오면 로그인으로, 로그아웃도 로그인으로", async () => {
    online();
    render(<Harness />);
    expect(await screen.findByText("실제 화면")).toBeInTheDocument();
    act(() => {
      for (const l of h.listeners) l();
    });
    expect(await screen.findByLabelText("비밀번호")).toBeInTheDocument();

    online();
    await act(async () => {
      await checkLiveAuth();
    });
    expect(screen.getByText("실제 화면")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "나가기" }));
    expect(m.signOut).toHaveBeenCalled();
    expect(await screen.findByLabelText("비밀번호")).toBeInTheDocument();
  });
});

describe("Workbench 안의 관문", () => {
  it("열 화면 모두 관문으로 감싸고 레일은 남는다", async () => {
    online();
    const { container } = render(<Workbench />);
    await waitFor(() => expect(getLiveAuthState()).toBe("ready"));
    const titles = [
      "대시보드",
      "진행 중인 점검",
      "끝난 점검",
      "새 점검",
      "자산 탐색",
      "탐색 결과",
      "접근통제 지도",
      "범위 확인",
      "점검 진행",
      "보고서",
    ];
    const views = [...container.querySelectorAll<HTMLElement>("section[data-view]")];
    views.forEach((v, i) => {
      expect(within(v).getByRole("heading", { level: 1, hidden: true })).toHaveTextContent(titles[i]);
    });
    expect(screen.getByRole("complementary", { name: "주 메뉴" })).toBeInTheDocument();
  });

  it("로그인 전에는 보이는 화면에 로그인 패널", async () => {
    m.readAuthStatus.mockResolvedValue({ initialized: true });
    m.restoreSession.mockResolvedValue(false);
    const { container } = render(<Workbench />);
    const dash = container.querySelector<HTMLElement>('section[data-view="dashboard"]') as HTMLElement;
    expect(await within(dash).findByLabelText("비밀번호")).toBeInTheDocument();
    expect(within(dash).getByRole("heading", { level: 1 })).toHaveTextContent("대시보드");
  });
});
