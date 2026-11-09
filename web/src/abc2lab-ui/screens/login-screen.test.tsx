import "@testing-library/jest-dom/vitest";

import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import LoginScreen from "./LoginScreen";

function json(status: number, body: unknown) {
  return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
}

let login: () => Response;
let status: () => Response;
let bodies: Array<{ username: string; password: string }>;

beforeEach(() => {
  localStorage.clear();
  bodies = [];
  login = () => json(200, { token: "issued" });
  status = () => json(200, { initialized: true });
  vi.stubGlobal("fetch", async (path: string, init: RequestInit = {}) => {
    if (path === "/api/auth/status") return status();
    bodies.push(JSON.parse(String(init.body)));
    return login();
  });
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

function fill(password: string) {
  fireEvent.change(screen.getByLabelText("비밀번호"), { target: { value: password } });
}
const submit = () => fireEvent.click(screen.getByRole("button", { name: "로그인" }));

describe("로그인 화면", () => {
  it("입력한 계정으로 로그인하고 완료를 알린다", async () => {
    const done = vi.fn();
    render(<LoginScreen onSignedIn={done} />);
    fireEvent.change(screen.getByLabelText("아이디"), { target: { value: "operator" } });
    fill("secret");
    submit();
    await waitFor(() => expect(done).toHaveBeenCalledTimes(1));
    expect(bodies).toEqual([{ username: "operator", password: "secret" }]);
    expect(localStorage.getItem("abc2lab.devToken")).toBe("issued");
  });

  it.each([
    [401, "아이디 또는 비밀번호가 맞지 않아요."],
    [429, "시도가 너무 많아요. 잠시 뒤 다시 시도해 주세요."],
    [500, "엔진에 연결하지 못했어요. 서버가 켜져 있는지 확인해 주세요."],
  ])("%i 응답이면 안내하고 다시 시도할 수 있게 둔다", async (code, message) => {
    const done = vi.fn();
    login = () => json(code, { error: "server text" });
    render(<LoginScreen onSignedIn={done} />);
    fill("wrong");
    submit();
    expect(await screen.findByRole("alert")).toHaveTextContent(message);
    expect(done).not.toHaveBeenCalled();
    expect(screen.getByRole("button", { name: "로그인" })).toBeEnabled();
  });

  it("비밀번호 미설정(403)은 서버 사유를 그대로 보여 준다", async () => {
    login = () => json(403, { error: "비밀번호가 아직 설정되지 않았습니다" });
    render(<LoginScreen onSignedIn={vi.fn()} />);
    fill("any");
    submit();
    expect(await screen.findByRole("alert")).toHaveTextContent("비밀번호가 아직 설정되지 않았습니다");
  });

  it("비밀번호가 비어 있으면 요청하지 않는다", async () => {
    render(<LoginScreen onSignedIn={vi.fn()} />);
    submit();
    await act(() => Promise.resolve());
    expect(bodies).toEqual([]);
  });

  it("아이디가 비어 있으면 요청하지 않는다", async () => {
    render(<LoginScreen onSignedIn={vi.fn()} />);
    fireEvent.change(screen.getByLabelText("아이디"), { target: { value: "" } });
    fill("secret");
    submit();
    await act(() => Promise.resolve());
    expect(bodies).toEqual([]);
  });

  it("응답을 기다리는 동안 다시 제출해도 한 번만 보낸다", async () => {
    let finish!: (response: Response) => void;
    vi.stubGlobal("fetch", async (path: string, init: RequestInit = {}) => {
      if (path === "/api/auth/status") return status();
      bodies.push(JSON.parse(String(init.body)));
      return new Promise<Response>((resolve) => {
        finish = resolve;
      });
    });
    const view = render(<LoginScreen onSignedIn={vi.fn()} />);
    fill("secret");
    const form = view.container.querySelector("form") as HTMLFormElement;
    fireEvent.submit(form);
    fireEvent.submit(form);
    await act(() => Promise.resolve());
    expect(bodies).toHaveLength(1);
    expect(screen.getByRole("button", { name: "확인 중…" })).toBeDisabled();
    await act(async () => finish(json(200, { token: "issued" })));
  });

  it("서버가 미설치 상태면 먼저 알린다", async () => {
    status = () => json(200, { initialized: false });
    render(<LoginScreen onSignedIn={vi.fn()} />);
    expect(await screen.findByRole("status")).toHaveTextContent("관리자 비밀번호가 아직 설정되지 않았어요");
  });

  it("설치가 끝난 서버에는 미설치 안내를 띄우지 않는다", async () => {
    render(<LoginScreen onSignedIn={vi.fn()} />);
    await act(() => Promise.resolve());
    expect(screen.queryByRole("status")).toBeNull();
  });

  it("상태 조회가 실패해도 로그인할 수 있다", async () => {
    const done = vi.fn();
    status = () => json(503, { error: "down" });
    render(<LoginScreen onSignedIn={done} />);
    fill("secret");
    submit();
    await waitFor(() => expect(done).toHaveBeenCalledTimes(1));
  });
});
