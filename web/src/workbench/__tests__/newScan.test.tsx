// 새 점검: 원본 nSync 의 모든 갈래(주소 상태 · 소유 확인 · 이름 · 전체 허용 · 요약)와 시작 · 주소 채우기.

import "@testing-library/jest-dom/vitest";
import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { hostState, initialForm, isInternal, newToken, normalizeUrl, nView, parseUrl, syncName } from "../lib/newScan";
import { type Shell, ShellContext, type View } from "../shell";
import { createInitialState, useWb } from "../store";
import { NewScan } from "../views/NewScan";

const noop = () => undefined;

function shellValue(over: Partial<Shell> = {}): Shell {
  return {
    route: "new",
    navSeq: 1,
    narrow: false,
    phone: false,
    reduce: false,
    inspOpen: false,
    railOpen: false,
    goTo: noop,
    openInsp: noop,
    closeInsp: noop,
    openRail: noop,
    closeRail: noop,
    dashPick: noop,
    ...over,
  };
}

function renderNew() {
  const goTo = vi.fn<(v: View) => void>();
  const r = render(
    <ShellContext.Provider value={shellValue({ goTo })}>
      <NewScan />
    </ShellContext.Provider>,
  );
  const $ = <T extends HTMLElement = HTMLElement>(sel: string) => r.container.querySelector<T>(sel) as T;
  const url = () => screen.getByLabelText("점검할 사이트 주소") as HTMLInputElement;
  const name = () => screen.getByLabelText(/작업 이름/) as HTMLInputElement;
  const starts = () => screen.getAllByRole("button", { name: "점검 시작" });
  const typeUrl = (v: string) => fireEvent.change(url(), { target: { value: v } });
  return { ...r, goTo, $, url, name, starts, typeUrl };
}

beforeEach(() => {
  useWb.setState(createInitialState());
});
afterEach(() => {
  cleanup();
  vi.useRealTimers();
  vi.restoreAllMocks();
});

describe("새 점검 계산 (원본 parseUrl · isInternal · hostState · nSync)", () => {
  it("parseUrl 은 스킴을 붙이고 이상한 값은 null", () => {
    expect(parseUrl("  ")).toBeNull();
    expect(parseUrl("http://")).toBeNull();
    expect(parseUrl("example.com/a")?.href).toBe("https://example.com/a");
    expect(parseUrl("HTTP://Example.com:8080")?.host).toBe("example.com:8080");
  });

  it("isInternal: localhost · ::1 · 점 없는 이름 · 사설 대역", () => {
    for (const h of [
      "localhost",
      "::1",
      "intranet",
      "127.0.0.1",
      "10.1.2.3",
      "192.168.0.9",
      "172.16.0.1",
      "172.31.9.9",
    ]) {
      expect(isInternal(h)).toBe(true);
    }
    for (const h of ["172.15.0.1", "172.32.0.1", "192.169.0.1", "8.8.8.8", "example.com"]) {
      expect(isInternal(h)).toBe(false);
    }
  });

  it("hostState 의 네 갈래", () => {
    expect(hostState("", {}).kind).toBe("bad");
    expect(hostState("localhost:3000", {})).toMatchObject({ ok: true, kind: "int", host: "localhost:3000" });
    expect(hostState("https://app.sample.io", {})).toMatchObject({ ok: true, kind: "allow" });
    expect(hostState("https://evil.test", {})).toMatchObject({ ok: false, kind: "ext", host: "evil.test" });
    expect(hostState("https://evil.test", { "evil.test": true })).toMatchObject({ ok: true, kind: "allow" });
  });

  it("처음 폼은 시작할 수 있고 요약이 원본과 같다", () => {
    const v = nView(initialForm(), {});
    expect(v.why).toEqual([]);
    expect(v.hostLine).toEqual({ kind: "pill", cls: "ok", pill: "허용 목록 · 예시" });
    expect(v.sum).toEqual({
      target: "https://shop.example.com",
      auth: "로그인 영역 포함 · 계정 2개 (아이디·비밀번호)",
      llm: "로컬 · qwen/qwen3.6-35b-a3b",
      depth: "표준 · 워커 3",
      goal: "주문·결제 권한 경계",
      send: "승인 후 전송",
    });
    expect(v.pol).toEqual({
      target: "shop.example.com · 허용 목록(예시)",
      send: "승인한 요청만 보냄",
      excl: "전역 3개 (/admin/delete* 등)",
    });
  });

  it("막는 까닭은 원본 순서대로 모인다", () => {
    const f = { ...initialForm(), url: "", name: " ", send: "full" as const };
    expect(nView(f, {}).why).toEqual([
      "주소를 입력하세요",
      "작업 이름을 입력하세요",
      "전체 허용은 시험환경 확인이 필요합니다",
    ]);
    expect(nView({ ...f, url: "https://evil.test", reset: true }, {}).why).toEqual([
      "외부 주소의 소유를 먼저 확인하세요",
      "작업 이름을 입력하세요",
    ]);
  });

  it("이름 채우기 · blur 정리 · 토큰 모양", () => {
    expect(syncName({ ...initialForm(), url: "app.sample.io" }).name).toBe("app.sample.io 점검");
    expect(syncName({ ...initialForm(), url: "app.sample.io", nameTouched: true }).name).toBe("shop.example.com 점검");
    const same = initialForm();
    expect(syncName(same)).toBe(same);
    expect(normalizeUrl("app.sample.io/x?y")).toBe("https://app.sample.io");
    expect(normalizeUrl("https://app.sample.io/x")).toBeNull();
    expect(newToken(() => 0.123456789)).toBe("abc2lab-verify=1f9add371f9add");
  });
});

describe("<NewScan />", () => {
  it("처음 화면: 허용 목록 주소, 시작 버튼 둘 다 켜짐, 요약", () => {
    const { $, url, name, starts } = renderNew();
    expect(screen.getByRole("heading", { level: 1, name: "새 점검" })).toHaveAttribute("tabindex", "-1");
    expect(url()).toHaveValue("https://shop.example.com");
    expect(url()).toHaveAttribute("aria-invalid", "false");
    expect($("#nHost").textContent).toBe("허용 목록 · 예시");
    expect($("#nVerify")).not.toBeVisible();
    expect(name()).toHaveValue("shop.example.com 점검");
    expect(name()).toHaveAttribute("aria-invalid", "false");
    expect(screen.getByText("작업 이름을 입력하세요.")).not.toBeVisible();
    for (const b of starts()) expect(b).toBeEnabled();
    expect($("#sTarget")).toHaveTextContent("https://shop.example.com");
    expect($("#nDepthHelp")).toBeNull();
    expect(screen.getByRole("button", { name: "요약 닫기" })).toBeInTheDocument();
  });

  it("주소 오류: 빈 값과 형식 오류", () => {
    const { $, url, starts, typeUrl } = renderNew();
    typeUrl("");
    expect($("#nHost")).toHaveTextContent("점검할 사이트 주소를 입력하세요.");
    expect(url()).toHaveAttribute("aria-invalid", "false");
    expect($("#pTarget")).toHaveTextContent("주소 없음");
    expect($("#sTarget")).toHaveTextContent("—");
    for (const b of starts()) expect(b).toBeDisabled();
    typeUrl("http://");
    expect($("#nHost")).toHaveTextContent("주소 형식을 확인하세요. 예: https://shop.example.com");
    expect(url()).toHaveAttribute("aria-invalid", "true");
    expect(screen.getAllByText("주소를 입력하세요")).toHaveLength(2);
  });

  it("내부 주소는 바로 점검할 수 있다", () => {
    const { $, name, typeUrl } = renderNew();
    typeUrl("localhost:3000");
    expect($("#nHost").textContent).toBe("내부 주소");
    expect($("#pTarget")).toHaveTextContent("localhost · 내부 주소");
    expect($("#sTarget")).toHaveTextContent("https://localhost:3000");
    expect(name()).toHaveValue("localhost 점검");
  });

  it("외부 주소: TXT 레코드 확인 → 1.3초 뒤 예시 통과, 주소 칸 포커스", () => {
    vi.useFakeTimers();
    const { $, url, name, starts, typeUrl } = renderNew();
    typeUrl("https://evil.test/path");
    expect($("#nHost").textContent).toBe("소유 확인 필요");
    expect($("#nVerify")).toBeVisible();
    expect($("#vHost")).toHaveTextContent("evil.test");
    expect($("#vName")).toHaveTextContent("_abc2lab-challenge.evil.test");
    const token = $("#vToken").textContent;
    expect(token).toMatch(/^abc2lab-verify=[0-9a-f]+$/);
    expect(name()).toHaveValue("evil.test 점검");
    expect($("#pTarget")).toHaveTextContent("evil.test · 소유 확인 필요");
    for (const b of starts()) expect(b).toBeDisabled();
    expect(screen.getAllByText("외부 주소의 소유를 먼저 확인하세요")).toHaveLength(2);

    // 다른 주소를 거쳐 와도 같은 토큰 (원본 tokenFor)
    typeUrl("https://shop.example.com");
    typeUrl("https://evil.test");
    expect($("#vToken").textContent).toBe(token);

    const check = screen.getByRole("button", { name: "TXT 레코드 확인" });
    fireEvent.click(check);
    expect(check).toBeDisabled();
    expect($("#vState")).toHaveTextContent("시안 · 실제 DNS 조회는 하지 않습니다");
    act(() => {
      vi.advanceTimersByTime(1300);
    });
    expect(check).toBeEnabled();
    expect($("#vState").textContent).toBe("시안이라 실제 확인 없이 예시로 통과했습니다");
    expect($("#nVerify")).not.toBeVisible();
    expect($("#nHost").textContent).toBe("허용 · 예시 통과(시안)");
    expect($("#pTarget")).toHaveTextContent("evil.test · 예시 통과(시안)");
    expect(document.activeElement).toBe(url());
    for (const b of starts()) expect(b).toBeEnabled();
  });

  it("작업 이름: 비우면 오류, 고친 뒤에는 주소를 따라가지 않고, 최근 주소 칩이 되돌린다", () => {
    const { url, name, starts, typeUrl } = renderNew();
    fireEvent.change(name(), { target: { value: "" } });
    expect(name()).toHaveAttribute("aria-invalid", "true");
    expect(screen.getByText("작업 이름을 입력하세요.")).toBeVisible();
    expect(screen.getAllByText("작업 이름을 입력하세요")).toHaveLength(2);
    for (const b of starts()) expect(b).toBeDisabled();
    fireEvent.change(name(), { target: { value: "내 점검" } });
    typeUrl("https://app.sample.io");
    expect(name()).toHaveValue("내 점검");
    fireEvent.click(screen.getByRole("button", { name: "docs.example.com 10/24" }));
    expect(url()).toHaveValue("https://docs.example.com");
    expect(name()).toHaveValue("docs.example.com 점검");
    expect(document.activeElement).toBe(url());
  });

  it("스킴 없이 적은 주소는 blur 때 origin 으로", () => {
    const { url, typeUrl } = renderNew();
    typeUrl("app.sample.io/x");
    fireEvent.blur(url());
    expect(url()).toHaveValue("https://app.sample.io");
  });

  it("로그인 · LLM · 범위 · 목표 · 제외 경로 · 전체 허용", () => {
    const { $ } = renderNew();
    fireEvent.click(screen.getByRole("radio", { name: "세션 직접 입력" }));
    expect($("#nForm1")).not.toBeVisible();
    expect($("#nForm2")).toBeVisible();
    expect($("#sAuth")).toHaveTextContent("로그인 영역 포함 · 계정 2개 (세션 직접 입력)");
    fireEvent.click(screen.getByRole("radio", { name: "공개 영역만" }));
    expect($("#nAuthBox")).not.toBeVisible();
    expect($("#sAuth")).toHaveTextContent("공개 영역만");

    fireEvent.click(screen.getByRole("radio", { name: "외부 API" }));
    expect($("#nLocal")).not.toBeVisible();
    expect($("#nLocalState")).not.toBeVisible();
    expect($("#nApi")).toBeVisible();
    fireEvent.change(screen.getByLabelText("제공자"), { target: { value: "Anthropic" } });
    expect($("#sLlm")).toHaveTextContent("외부 API · Anthropic");

    fireEvent.click(screen.getByRole("radio", { name: "제한 없음" }));
    fireEvent.change(screen.getByLabelText("워커 수"), { target: { value: "8" } });
    expect($("#sDepth")).toHaveTextContent("제한 없음 · 워커 8");

    fireEvent.click(screen.getByRole("checkbox", { name: "주문·결제 권한 경계" }));
    expect($("#sGoal")).toHaveTextContent("기본 목표");
    fireEvent.click(screen.getByRole("checkbox", { name: "비로그인 노출" }));
    fireEvent.click(screen.getByRole("checkbox", { name: "관리자 기능 접근" }));
    expect($("#sGoal")).toHaveTextContent("관리자 기능 접근, 비로그인 노출");

    fireEvent.change(screen.getByLabelText("제외 경로 · 선택"), { target: { value: " /a \n\n/b/*\n" } });
    expect($("#pExcl")).toHaveTextContent("전역 3개 (/admin/delete* 등) + 이 점검 2개");

    fireEvent.click(screen.getByRole("radio", { name: /전체 허용/ }));
    expect($("#nFullWarn")).toBeVisible();
    expect($("#sSend")).toHaveTextContent("전체 허용 · 시험환경 전용");
    expect($("#pSend")).toHaveTextContent("승인 없이 보냄 · 시험환경 확인 필요");
    for (const b of screen.getAllByRole("button", { name: "점검 시작" })) expect(b).toBeDisabled();
    fireEvent.click(screen.getByRole("checkbox", { name: "이 대상은 리셋할 수 있는 시험환경입니다" }));
    for (const b of screen.getAllByRole("button", { name: "점검 시작" })) expect(b).toBeEnabled();
  });

  it("점검 시작 → startScan(origin) 후 자산 탐색으로 (폼 제출 · 요약 버튼 모두)", () => {
    const { starts, typeUrl, goTo } = renderNew();
    typeUrl("https://app.sample.io/some/path");
    fireEvent.click(starts()[1]);
    expect(useWb.getState().target).toBe("https://app.sample.io");
    expect(useWb.getState().railNew).toBe("crawl");
    expect(useWb.getState().crawl.started).toBe(false);
    expect(goTo).toHaveBeenCalledWith("crawl");
    fireEvent.submit(screen.getByLabelText("점검할 사이트 주소").closest("form") as HTMLFormElement);
    expect(goTo).toHaveBeenCalledTimes(2);
  });

  it("막혀 있으면 제출해도 시작하지 않는다", () => {
    const { url, typeUrl, goTo } = renderNew();
    typeUrl("https://evil.test");
    fireEvent.submit(url().closest("form") as HTMLFormElement);
    expect(goTo).not.toHaveBeenCalled();
    expect(useWb.getState().railNew).toBeNull();
  });

  it("다른 화면의 주소 채우기 요청(prefill.seq)을 한 번씩 반영한다", () => {
    const { url, name } = renderNew();
    fireEvent.change(name(), { target: { value: "직접 쓴 이름" } });
    act(() => useWb.getState().nPrefill("https://app.sample.io"));
    expect(url()).toHaveValue("https://app.sample.io");
    expect(name()).toHaveValue("app.sample.io 점검");
    fireEvent.change(url(), { target: { value: "https://blog.example.com" } });
    act(() => useWb.getState().nPrefill("https://app.sample.io"));
    expect(url()).toHaveValue("https://app.sample.io");
  });
});
