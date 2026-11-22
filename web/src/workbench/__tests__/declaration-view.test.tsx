import "@testing-library/jest-dom/vitest";
import { useContext } from "react";

import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

vi.mock("@/lib/diagnosis/api", async (importOriginal) => {
  const real = await importOriginal<typeof import("@/lib/diagnosis/api")>();
  return { ...real, apiFetch: vi.fn() };
});

import { ApiError, apiFetch } from "@/lib/diagnosis/api";

import { ShellContext, type View } from "../shell";
import { PolicyDeclarationLive } from "../views/live/PolicyDeclarationLive";

const api = vi.mocked(apiFetch);
afterEach(() => {
  cleanup();
  api.mockReset();
});

// 합성 대상·선언(실제 앱 경로 아님)
const TARGETS = {
  targets: [
    {
      id: "t1",
      origin: "https://a.example",
      state: "approved",
      revision: 3,
      path_prefixes: ["/api"],
      methods: ["GET", "POST"],
    },
    {
      id: "t2",
      origin: "https://b.example",
      state: "approved",
      revision: 5,
      path_prefixes: ["/api"],
      methods: ["GET"],
    },
    { id: "t3", origin: "https://c.example", state: "pending", revision: 1 },
  ],
};
const stored = (type = "note", by = "operator-1", ref = "t1") => ({
  version: "e3-product-declaration/v1",
  target_ref: ref,
  target_approval_revision: "3",
  declared_by: by,
  declared_at: "2026-11-18T00:00:00.000000Z",
  identity: { path: "/api/me", subject_id_path: "id" },
  types: [
    {
      type,
      sharing: "owner_only",
      owner_only_fields: ["body"],
      create: {
        path: "/api/notes",
        method: "POST",
        body_kind: "json",
        body_template: '{"title":"t"}',
        marker_field: "body",
        id_path: "id",
      },
      read: { path_template: "/api/notes/{id}", data_path: "." },
    },
  ],
});

type Reply = unknown | (() => Promise<unknown>);
/** 경로·메서드별 응답. 함수면 그대로 호출(보류·실패용). */
function serve(map: Record<string, Reply[]>) {
  api.mockImplementation(((path: string, init?: RequestInit) => {
    const key = `${init?.method ?? "GET"} ${path}`;
    const queue = map[key];
    if (!queue?.length) return Promise.reject(new Error(`unexpected ${key}`));
    const next = queue.length > 1 ? queue.shift() : queue[0];
    return typeof next === "function" ? (next as () => Promise<unknown>)() : Promise.resolve(next);
  }) as never);
}
const GET_T = "GET /api/product/targets";
const GET_D = (id: string) => `GET /api/product/targets/${id}/declaration`;
/** 단건 대상(경로·메서드 포함) — 목록 응답은 서버가 경로·메서드를 비워 보낸다(1차 리허설 F3) */
const GET_ONE = (id: string) => `GET /api/product/targets/${id}`;
const ONE = (id: string) => {
  const t = TARGETS.targets.find((x) => x.id === id) as Record<string, unknown>;
  return { target: { id, revision: t.revision, path_prefixes: t.path_prefixes ?? [], methods: t.methods ?? [] } };
};
const PUT_D = (id: string) => `PUT /api/product/targets/${id}/declaration`;
const reject =
  (status: number, code = "x", field?: string) =>
  () =>
    Promise.reject(new ApiError(status, code, field));
type Json = Record<string, unknown>;
/** #705 조회 응답: 최상위 감사·판 칸은 안쪽 선언 레코드와 같은 값. */
const active = (decl: Json = stored()) => ({
  state: "active",
  target_revision: Number(decl.target_approval_revision),
  declared_by: decl.declared_by,
  declared_at: decl.declared_at,
  declaration: decl,
});
const invalidRes = (reason: string, decl: Json = stored()) => ({
  ...active(decl),
  state: "invalid",
  invalid_reason: reason,
});
const NONE = { state: "none", declaration: null };
const pending = () => new Promise<never>(() => undefined);

function mount(route: View = "declaration") {
  function At({ r }: { r: View }) {
    const shell = useContext(ShellContext);
    return (
      <ShellContext.Provider value={{ ...shell, route: r }}>
        <PolicyDeclarationLive />
      </ShellContext.Provider>
    );
  }
  const view = render(<At r={route} />);
  return { at: (r: View) => view.rerender(<At r={r} />) };
}
async function pick(id: string) {
  fireEvent.change(await screen.findByLabelText("선언할 대상"), { target: { value: id } });
}
const type = (label: string, value: string) => fireEvent.change(screen.getByLabelText(label), { target: { value } });

/** 빈 선언에서 정상 소유자 전용 종류 하나를 채운다. */
function fillValid() {
  type("신원 확인 경로", "/api/me");
  type("사용자 식별자 위치", "id");
  type("자원 종류 1 이름", "note");
  type("자원 종류 1 소유자 전용 필드", "body");
  type("자원 종류 1 생성 경로", "/api/notes");
  type("자원 종류 1 본문 틀", '{"title":"t"}');
  type("자원 종류 1 표식 칸", "body");
  type("자원 종류 1 객체 ID 위치", "id");
  type("자원 종류 1 조회 경로 틀", "/api/notes/{id}");
}

describe("PolicyDeclarationLive (대상 · 조회 상태)", () => {
  it("승인된 대상만 고를 수 있고, 현재 선언을 서버 값 그대로 보이고 입력에 채운다", async () => {
    serve({ [GET_T]: [TARGETS], [GET_D("t1")]: [active(stored())] });
    mount();
    const select = await screen.findByLabelText("선언할 대상");
    expect(select.querySelectorAll("option")).toHaveLength(3); // 선택… + 승인 2개
    await pick("t1");
    expect(await screen.findByText(/선언자/)).toHaveTextContent("operator-1");
    expect(screen.getByText(/시험 대상 1개/)).toBeInTheDocument();
    expect(screen.getByLabelText("자원 종류 1 이름")).toHaveValue("note");
    expect(screen.getByText("확정 단계 시험 대상")).toBeInTheDocument();
  });

  it("선언 없음은 빈 입력, 저장 라우트가 없으면 '저장 기능 없음'으로 선언 없음과 다르게 보인다", async () => {
    serve({ [GET_T]: [TARGETS], [GET_D("t1")]: [NONE], [GET_D("t2")]: [reject(404)] });
    mount();
    await pick("t1");
    expect(await screen.findByText(/아직 선언이 없습니다/)).toBeInTheDocument();
    expect(screen.getByLabelText("자원 종류 1 이름")).toHaveValue("");
    await pick("t2");
    expect(await screen.findByText(/서버에 선언 저장 기능이 아직 없습니다/)).toBeInTheDocument();
    expect(screen.queryByText(/아직 선언이 없습니다/)).toBeNull();
    // 기존 선언을 확인하지 못했으니 입력을 열지 않는다
    expect(screen.queryByLabelText("신원 확인 경로")).toBeNull();
  });

  it("기존 선언 모양을 확인 못 하면 입력 · 저장을 잠그고, 다시 읽어 없음이 확인되면 빈 입력으로 연다", async () => {
    serve({ [GET_T]: [TARGETS], [GET_D("t1")]: [active({ ...stored(), extra: 1 }), NONE] });
    mount();
    await pick("t1");
    expect(await screen.findByText(/모양을 확인할 수 없어 표시하지 않습니다/)).toBeInTheDocument();
    expect(screen.queryByLabelText("신원 확인 경로")).toBeNull();
    expect(screen.queryByRole("button", { name: "선언 저장" })).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "다시 읽기" }));
    expect(await screen.findByText(/아직 선언이 없습니다/)).toBeInTheDocument();
    expect(screen.getByLabelText("자원 종류 1 이름")).toHaveValue("");
    expect(api.mock.calls.some(([, init]) => init?.method === "PUT")).toBe(false);
  });

  it("다른 대상의 선언 응답은 이 대상 화면에 보이지도 입력을 채우지도 않는다", async () => {
    serve({ [GET_T]: [TARGETS], [GET_D("t1")]: [active(stored("memo", "operator-1", "t2"))] });
    mount();
    await pick("t1");
    expect(await screen.findByText(/모양을 확인할 수 없어 표시하지 않습니다/)).toBeInTheDocument();
    expect(screen.queryByText(/선언자/)).toBeNull();
    expect(screen.queryByDisplayValue("memo")).toBeNull();
    expect(screen.queryByLabelText("자원 종류 1 이름")).toBeNull();
  });

  it("조회 실패는 '선언 없음'이 아니고 다시 읽기로 같은 대상을 다시 부른다", async () => {
    serve({ [GET_T]: [TARGETS], [GET_D("t1")]: [reject(503), active(stored())] });
    mount();
    await pick("t1");
    expect(await screen.findByText(/선언이 없다는 뜻이 아닙니다/)).toBeInTheDocument();
    expect(screen.queryByLabelText("신원 확인 경로")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "다시 읽기" }));
    expect(await screen.findByText(/선언자/)).toBeInTheDocument();
  });

  it("모양이 깨진 저장본은 보이지 않고(fail-closed), 무효는 사유를 보인다", async () => {
    serve({
      [GET_T]: [TARGETS],
      [GET_D("t1")]: [active({ ...stored(), extra: 1 })],
      [GET_D("t2")]: [invalidRes("target_reapproved", stored("memo", "operator-1", "t2"))],
    });
    mount();
    await pick("t1");
    expect(await screen.findByText(/모양을 확인할 수 없어 표시하지 않습니다/)).toBeInTheDocument();
    expect(screen.queryByText(/선언자/)).toBeNull();
    await pick("t2");
    expect(await screen.findByText(/대상을 다시 승인해서 이전 선언이 무효가/)).toBeInTheDocument();
    // 무효 선언은 입력에 채우지 않는다(다시 선언)
    expect(screen.getByLabelText("자원 종류 1 이름")).toHaveValue("");
  });

  it("대상을 바꾸면 이전 대상의 선언은 새 응답 전까지 보이지 않는다", async () => {
    serve({
      [GET_T]: [TARGETS],
      [GET_D("t1")]: [active(stored())],
      [GET_D("t2")]: [pending],
    });
    mount();
    await pick("t1");
    await screen.findByText(/선언자/);
    await pick("t2");
    expect(screen.queryByText(/선언자/)).toBeNull();
    expect(screen.getByText("불러오는 중…")).toBeInTheDocument();
    expect(screen.queryByLabelText("신원 확인 경로")).toBeNull();
  });
});

describe("PolicyDeclarationLive (입력 · 저장)", () => {
  it("빈 선언을 채워 저장하면 PUT 하나를 보내고, 서버 값을 다시 읽어 보인다", async () => {
    serve({
      [GET_T]: [TARGETS],
      [GET_D("t1")]: [NONE, active(stored())],
      [PUT_D("t1")]: [undefined],
    });
    mount();
    await pick("t1");
    await screen.findByText(/아직 선언이 없습니다/);
    fillValid();
    await act(async () => fireEvent.click(screen.getByRole("button", { name: "선언 저장" })));
    const put = api.mock.calls.find(([, init]) => init?.method === "PUT");
    expect(put?.[0]).toBe("/api/product/targets/t1/declaration");
    const body = JSON.parse(String(put?.[1]?.body));
    expect(Object.keys(body).sort()).toEqual(["declaration", "target_revision"]);
    expect(body.target_revision).toBe(3);
    expect(body.declaration).not.toHaveProperty("declared_by");
    expect(body.declaration).not.toHaveProperty("version");
    expect(body.declaration.types[0]).toMatchObject({ type: "note", owner_only_fields: ["body"] });
    expect(await screen.findByText(/선언자/)).toHaveTextContent("operator-1");
    expect(screen.getByRole("status")).toHaveTextContent("저장했습니다");
  });

  it("저장을 기다리는 동안 고친 입력은 저장 · 재조회가 끝나도 덮지 않는다", async () => {
    let finishPut: (v: unknown) => void = () => undefined;
    serve({
      [GET_T]: [TARGETS],
      [GET_D("t1")]: [NONE, active(stored("first"))],
      [PUT_D("t1")]: [() => new Promise((ok) => (finishPut = ok))],
    });
    mount();
    await pick("t1");
    await screen.findByText(/아직 선언이 없습니다/);
    fillValid();
    type("자원 종류 1 이름", "first");
    fireEvent.click(screen.getByRole("button", { name: "선언 저장" }));
    expect(screen.getByRole("button", { name: "저장 중…" })).toBeDisabled();
    type("자원 종류 1 이름", "newer");
    await act(async () => finishPut(undefined));
    // 재조회한 서버 값(first)은 요약에 보이고, 입력은 그대로 newer
    expect(await screen.findByText(/선언자/)).toBeInTheDocument();
    expect(screen.getByLabelText("자원 종류 1 이름")).toHaveValue("newer");
    expect(screen.getByRole("status")).toHaveTextContent("아직 저장 전");
    expect(screen.getByRole("status")).not.toHaveTextContent("다시 읽어 보입니다");
  });

  it("쿠키 헤더를 붙여넣은 본문 틀은 저장 요청을 보내지 않는다(값 원문은 화면 문구에 없다)", async () => {
    serve({ [GET_T]: [TARGETS], [GET_D("t1")]: [NONE], [PUT_D("t1")]: [undefined] });
    mount();
    await pick("t1");
    await screen.findByText(/아직 선언이 없습니다/);
    fillValid();
    type("자원 종류 1 본문 틀", '{"title":"t","note":"Cookie: sid=synthetic-cookie"}');
    expect(screen.getByText(/자격 값 모양/)).toBeInTheDocument();
    const btn = screen.getByRole("button", { name: "선언 저장" });
    expect(btn).toBeDisabled();
    await act(async () => fireEvent.click(btn));
    expect(api.mock.calls.some(([, init]) => init?.method === "PUT")).toBe(false);
    const alerts = screen
      .getAllByRole("alert")
      .map((a) => a.textContent ?? "")
      .join(" ");
    expect(alerts).not.toContain("synthetic-cookie");
  });

  it("편집 중인 아주 깊은 본문 틀도 화면이 깨지지 않고 크기 오류로 저장을 막는다(#754 리뷰)", async () => {
    serve({ [GET_T]: [TARGETS], [GET_D("t1")]: [NONE] });
    mount();
    await pick("t1");
    await screen.findByText(/아직 선언이 없습니다/);
    fillValid();
    // 여는 괄호만 계속 친 미완성 틀 — 재귀 검사에 넣으면 스택이 넘치던 입력
    expect(() => type("자원 종류 1 본문 틀", "[".repeat(200_000))).not.toThrow();
    expect(screen.getByText(/본문 틀은 2048바이트 이하입니다/)).toBeInTheDocument();
    const btn = screen.getByRole("button", { name: "선언 저장" });
    expect(btn).toBeDisabled();
    await act(async () => fireEvent.click(btn));
    expect(api.mock.calls.some(([, init]) => init?.method === "PUT")).toBe(false);
  });

  it("칸 오류는 고친 뒤 보이고, 오류가 있으면 저장하지 않는다", async () => {
    serve({ [GET_T]: [TARGETS], [GET_D("t1")]: [NONE] });
    mount();
    await pick("t1");
    await screen.findByText(/아직 선언이 없습니다/);
    expect(screen.queryAllByRole("alert")).toHaveLength(0); // 손대기 전엔 오류 없음
    type("자원 종류 1 조회 경로 틀", "/api/notes");
    expect(screen.getByText(/\{id\} 조각이 정확히 하나인/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "선언 저장" })).toBeDisabled();
    expect(api.mock.calls.some(([, init]) => init?.method === "PUT")).toBe(false);
  });

  it("본문 틀에 자격 칸이 있으면 저장을 막고 이름만 알린다(값 원문은 화면 문구에 없다)", async () => {
    serve({ [GET_T]: [TARGETS], [GET_D("t1")]: [NONE] });
    mount();
    await pick("t1");
    await screen.findByText(/아직 선언이 없습니다/);
    fillValid();
    type("자원 종류 1 본문 틀", '{"title":"t","password":"synthetic-pw"}');
    expect(screen.getByText(/자격으로 보이는 칸이 있어 저장하지 않습니다\(password\)/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "선언 저장" })).toBeDisabled();
    const alerts = screen
      .getAllByRole("alert")
      .map((a) => a.textContent ?? "")
      .join(" ");
    expect(alerts).not.toContain("synthetic-pw");
  });

  it("두 종류 이름에 같은 자격 값을 넣어도 경고에 값이 다시 보이지 않는다", async () => {
    serve({ [GET_T]: [TARGETS], [GET_D("t1")]: [NONE] });
    mount();
    await pick("t1");
    await screen.findByText(/아직 선언이 없습니다/);
    fillValid();
    fireEvent.click(screen.getByRole("button", { name: "자원 종류 추가" }));
    type("자원 종류 1 이름", "Bearer synthetic-bearer-value");
    type("자원 종류 2 이름", "Bearer synthetic-bearer-value");
    expect(screen.getByText("자원 종류 이름이 1번 종류와 겹칩니다.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "선언 저장" })).toBeDisabled();
    // 입력란 밖(경고 · 배지 · 안내)에는 값이 없다
    const outside = [...document.body.querySelectorAll("*")]
      .filter((el) => el.children.length === 0 && !(el instanceof HTMLInputElement))
      .map((el) => el.textContent ?? "")
      .join(" ");
    expect(outside).not.toContain("synthetic-bearer-value");
    expect(api.mock.calls.some(([, init]) => init?.method === "PUT")).toBe(false);
  });

  it("승인 범위 밖은 참고 경고만 하고 저장은 막지 않는다", async () => {
    serve({ [GET_T]: [TARGETS], [GET_ONE("t2")]: [ONE("t2")], [GET_D("t2")]: [NONE], [PUT_D("t2")]: [reject(403)] });
    mount();
    await pick("t2");
    await screen.findByText(/아직 선언이 없습니다/);
    fillValid();
    expect(screen.getByText(/대상 승인에 POST 가 없습니다/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "선언 저장" })).toBeEnabled();
    await act(async () => fireEvent.click(screen.getByRole("button", { name: "선언 저장" })));
    expect(await screen.findByText(/이 대상의 승인자만 선언할 수 있습니다/)).toBeInTheDocument();
  });

  it.each<[number, string, RegExp]>([
    [404, "HTTP 404", /서버에 선언 저장 기능이 아직 없습니다\. 입력은/],
    [404, "target_not_found", /이 대상을 찾지 못했습니다/],
    [409, "target_not_approved", /지금 승인 상태가 아닙니다/],
    [409, "declaration_target_revision_mismatch", /그사이 대상을 다시 승인해 판이 바뀌었습니다/],
    [400, "invalid_declaration", /서버가 선언 형식을 받지 않았습니다/],
    [500, "x", /선언을 저장하지 못했습니다/],
  ])("저장 %i %s → 문구, 입력은 남는다", async (status, code, text) => {
    serve({ [GET_T]: [TARGETS], [GET_D("t1")]: [NONE], [PUT_D("t1")]: [reject(status, code)] });
    mount();
    await pick("t1");
    await screen.findByText(/아직 선언이 없습니다/);
    fillValid();
    await act(async () => fireEvent.click(screen.getByRole("button", { name: "선언 저장" })));
    expect(await screen.findByText(text)).toBeInTheDocument();
    expect(screen.getByLabelText("자원 종류 1 이름")).toHaveValue("note");
  });

  it("다른 화면에 갔다 와도 저장 안 한 입력을 서버 값으로 덮지 않는다", async () => {
    serve({ [GET_T]: [TARGETS], [GET_D("t1")]: [active(stored())] });
    const v = mount();
    await pick("t1");
    await screen.findByText(/선언자/);
    type("자원 종류 1 이름", "memo");
    act(() => v.at("dashboard"));
    act(() => v.at("declaration"));
    expect(await screen.findByText(/선언자/)).toBeInTheDocument();
    expect(screen.getByLabelText("자원 종류 1 이름")).toHaveValue("memo");
  });

  it("공유로 바꾸면 요청 칸을 숨기고 '건너뜀'으로 보인다, 종류 추가 · 빼기", async () => {
    serve({ [GET_T]: [TARGETS], [GET_D("t1")]: [NONE] });
    mount();
    await pick("t1");
    await screen.findByText(/아직 선언이 없습니다/);
    type("자원 종류 1 공유 방식", "shared");
    expect(screen.getByText("공유 — 시험하지 않음(건너뜀)")).toBeInTheDocument();
    expect(screen.queryByLabelText("자원 종류 1 생성 경로")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "자원 종류 추가" }));
    expect(screen.getByLabelText("자원 종류 2 이름")).toBeInTheDocument();
    fireEvent.click(screen.getAllByRole("button", { name: "종류 빼기" })[0]);
    expect(screen.queryByLabelText("자원 종류 2 이름")).toBeNull();
  });

  it("내보내기 · 공유 · 모델로 보내기 버튼은 없다", async () => {
    serve({ [GET_T]: [TARGETS], [GET_D("t1")]: [active(stored())] });
    mount();
    await pick("t1");
    await screen.findByText(/선언자/);
    for (const el of screen.queryAllByRole("button"))
      expect(el.textContent ?? "").not.toMatch(/내보내기|공유하기|export|모델/i);
  });

  it("화면이 숨겨져 있으면 대상도 선언도 읽지 않는다", () => {
    serve({ [GET_T]: [TARGETS] });
    mount("dashboard");
    expect(api).not.toHaveBeenCalled();
  });
});

describe("PolicyDeclarationLive (#705 응답 모양)", () => {
  it("서버가 짚은 칸을 이름만 보이고 그 칸 옆에도 알린다(값 원문 없음)", async () => {
    serve({
      [GET_T]: [TARGETS],
      [GET_D("t1")]: [NONE],
      [PUT_D("t1")]: [reject(400, "invalid_declaration", "types[0].create.body_template")],
    });
    mount();
    await pick("t1");
    await screen.findByText(/아직 선언이 없습니다/);
    fillValid();
    await act(async () => fireEvent.click(screen.getByRole("button", { name: "선언 저장" })));
    expect(await screen.findByText(/문제 칸:/)).toHaveTextContent("types[0].create.body_template");
    expect(screen.getByText(/서버가 이 칸을 받지 않았습니다\(types\[0\]\.create\.body_template\)/)).toBeInTheDocument();
    // 고치면 서버 지적은 지운다
    type("자원 종류 1 본문 틀", '{"title":"u"}');
    expect(screen.queryByText(/서버가 이 칸을 받지 않았습니다/)).toBeNull();
  });

  it("조회 404 target_not_found 는 '대상 없음'이고 입력을 열지 않는다(선언 없음·저장 기능 없음과 다름)", async () => {
    serve({ [GET_T]: [TARGETS], [GET_D("t1")]: [reject(404, "target_not_found")] });
    mount();
    await pick("t1");
    expect(await screen.findByText(/이 대상을 찾지 못했습니다/)).toBeInTheDocument();
    expect(screen.queryByText(/아직 선언이 없습니다/)).toBeNull();
    expect(screen.queryByText(/저장 기능이 아직 없습니다/)).toBeNull();
    expect(screen.queryByLabelText("신원 확인 경로")).toBeNull();
  });

  it("승인 만료로 무효가 된 선언은 사유를 보인다", async () => {
    serve({ [GET_T]: [TARGETS], [GET_D("t1")]: [invalidRes("target_expired")] });
    mount();
    await pick("t1");
    expect(await screen.findByText(/대상 승인이 만료돼 이전 선언이 무효가/)).toBeInTheDocument();
  });
});

describe("PolicyDeclarationLive (확정 단계 계정 역할, 계약 §4)", () => {
  const putBody = () => {
    const put = api.mock.calls.find(([, init]) => init?.method === "PUT");
    return JSON.parse(String(put?.[1]?.body)) as { declaration: Record<string, unknown> };
  };

  it("두 계정과 등급 확인을 입력해 저장하면 본문에 세 칸을 싣고, 상태 문구가 바뀐다", async () => {
    serve({ [GET_T]: [TARGETS], [GET_D("t1")]: [NONE, active(stored())], [PUT_D("t1")]: [undefined] });
    mount();
    await pick("t1");
    await screen.findByText(/아직 선언이 없습니다/);
    fillValid();
    expect(screen.getByText(/계정 역할을 정하지 않았습니다/)).toBeInTheDocument();
    type("소유 · 생성 계정", "tester-a");
    type("교차 조회 계정", "tester-b");
    expect(screen.getByText(/같은 권한 등급이라는 확인이 없습니다/)).toBeInTheDocument();
    fireEvent.click(screen.getByLabelText("두 계정이 같은 권한 등급"));
    expect(screen.getByText(/계정 역할 정함/)).toBeInTheDocument();
    await act(async () => fireEvent.click(screen.getByRole("button", { name: "선언 저장" })));
    expect(putBody().declaration).toMatchObject({
      owner_account_label: "tester-a",
      cross_account_label: "tester-b",
      accounts_same_privilege: true,
    });
  });

  it("역할이 든 저장본은 입력 칸에 채워지고, 다른 칸만 고쳐 다시 저장해도 역할을 지우지 않는다", async () => {
    const withRoles = { ...stored(), owner_account_label: "tester-a", cross_account_label: "tester-b" };
    serve({ [GET_T]: [TARGETS], [GET_D("t1")]: [active(withRoles)], [PUT_D("t1")]: [undefined] });
    mount();
    await pick("t1");
    // 요약: 서버 값 그대로, 등급 확인이 없으니 확정 단계 꺼짐
    await screen.findByText(/선언자/);
    const summary = screen.getByRole("region", { name: "현재 선언" });
    expect(summary).toHaveTextContent("소유 · 생성 계정 tester-a · 교차 조회 계정 tester-b");
    expect(summary).toHaveTextContent("같은 권한 등급이라는 확인이 없습니다");
    expect(screen.getByLabelText("소유 · 생성 계정")).toHaveValue("tester-a");
    expect(screen.getByLabelText("교차 조회 계정")).toHaveValue("tester-b");
    expect(screen.getByLabelText("두 계정이 같은 권한 등급")).not.toBeChecked();
    type("신원 확인 경로", "/api/whoami");
    await act(async () => fireEvent.click(screen.getByRole("button", { name: "선언 저장" })));
    const body = putBody().declaration;
    expect(body).toMatchObject({ owner_account_label: "tester-a", cross_account_label: "tester-b" });
    expect(body).not.toHaveProperty("accounts_same_privilege");
  });

  it("한쪽만 적으면 그 칸에 문제를 보이고 저장 요청을 보내지 않는다(문구에 입력 원문 없음)", async () => {
    serve({ [GET_T]: [TARGETS], [GET_D("t1")]: [NONE], [PUT_D("t1")]: [undefined] });
    mount();
    await pick("t1");
    await screen.findByText(/아직 선언이 없습니다/);
    fillValid();
    type("소유 · 생성 계정", "tester-a");
    await act(async () => fireEvent.click(screen.getByRole("button", { name: "선언 저장" })));
    expect(api.mock.calls.some(([, init]) => init?.method === "PUT")).toBe(false);
    const alert = screen.getByText(/둘 다 적거나 둘 다 비웁니다/);
    expect(alert).toHaveAttribute("role", "alert");
    expect(alert).not.toHaveTextContent("tester-a");
  });
});

describe("PolicyDeclarationLive (객체 ID 출처, #757 · #766)", () => {
  const putCreate = () => {
    const put = api.mock.calls.find(([, init]) => init?.method === "PUT");
    const body = JSON.parse(String(put?.[1]?.body)) as { declaration: { types: { create: Json }[] } };
    return body.declaration.types[0].create;
  };
  const ID_SOURCE = "자원 종류 1 객체 ID 출처";
  const ID_NAME = "자원 종류 1 객체 ID 위치";
  // 합성 선언: 클라이언트가 ID를 정하는 API(요청 본문 칸 ID)
  const storedIdField = () => {
    const d = stored();
    d.types[0].create = { ...d.types[0].create, id_path: "", id_field: "title_id" } as (typeof d.types)[0]["create"];
    return d;
  };

  it("요청 본문 칸으로 골라 저장하면 id_field만 싣고 id_path는 빈 값이다", async () => {
    serve({ [GET_T]: [TARGETS], [GET_D("t1")]: [NONE, active(storedIdField())], [PUT_D("t1")]: [undefined] });
    mount();
    await pick("t1");
    await screen.findByText(/아직 선언이 없습니다/);
    fillValid();
    expect(screen.getByLabelText(ID_SOURCE)).toHaveValue("path");
    type(ID_SOURCE, "field");
    // 적던 이름은 새 출처로 옮긴다(두 칸이 같이 남지 않음)
    expect(screen.getByLabelText(ID_NAME)).toHaveValue("id");
    expect(screen.getByText("객체 ID — 요청 본문 칸")).toBeInTheDocument();
    type(ID_NAME, "title_id");
    await act(async () => fireEvent.click(screen.getByRole("button", { name: "선언 저장" })));
    expect(putCreate()).toMatchObject({ id_path: "", id_field: "title_id" });
  });

  it("응답 경로 선언은 저장 본문에 id_field 칸이 없다", async () => {
    serve({ [GET_T]: [TARGETS], [GET_D("t1")]: [NONE, active(stored())], [PUT_D("t1")]: [undefined] });
    mount();
    await pick("t1");
    await screen.findByText(/아직 선언이 없습니다/);
    fillValid();
    await act(async () => fireEvent.click(screen.getByRole("button", { name: "선언 저장" })));
    expect(putCreate()).toMatchObject({ id_path: "id" });
    expect(putCreate()).not.toHaveProperty("id_field");
  });

  it("id_field 저장본은 '요청 본문 칸'으로 채워지고, 다른 칸만 고쳐 다시 저장해도 지우지 않는다", async () => {
    serve({ [GET_T]: [TARGETS], [GET_D("t1")]: [active(storedIdField())], [PUT_D("t1")]: [undefined] });
    mount();
    await pick("t1");
    await screen.findByText(/선언자/);
    expect(screen.getByLabelText(ID_SOURCE)).toHaveValue("field");
    expect(screen.getByLabelText(ID_NAME)).toHaveValue("title_id");
    type("신원 확인 경로", "/api/whoami");
    await act(async () => fireEvent.click(screen.getByRole("button", { name: "선언 저장" })));
    expect(putCreate()).toMatchObject({ id_path: "", id_field: "title_id" });
  });

  it("id_field 저장본에서 ID 칸을 다 지웠다 다시 쳐도 출처는 '요청 본문 칸'이고 id_field로 저장한다 (#775 리뷰)", async () => {
    serve({ [GET_T]: [TARGETS], [GET_D("t1")]: [active(storedIdField())], [PUT_D("t1")]: [undefined] });
    mount();
    await pick("t1");
    await screen.findByText(/선언자/);
    expect(screen.getByLabelText(ID_SOURCE)).toHaveValue("field");
    type(ID_NAME, "");
    // 비운 순간에도 출처는 그대로(응답 경로로 떨어지지 않음)
    expect(screen.getByLabelText(ID_SOURCE)).toHaveValue("field");
    expect(screen.getByText("객체 ID — 요청 본문 칸")).toBeInTheDocument();
    type(ID_NAME, "book_title");
    expect(screen.getByLabelText(ID_SOURCE)).toHaveValue("field");
    await act(async () => fireEvent.click(screen.getByRole("button", { name: "선언 저장" })));
    expect(putCreate()).toMatchObject({ id_path: "", id_field: "book_title" });
  });

  it("응답 경로로 친 이름을 다 지웠다 다시 쳐도 출처는 '응답 경로'다", async () => {
    serve({ [GET_T]: [TARGETS], [GET_D("t1")]: [NONE], [PUT_D("t1")]: [undefined] });
    mount();
    await pick("t1");
    await screen.findByText(/아직 선언이 없습니다/);
    fillValid();
    type(ID_SOURCE, "field");
    type(ID_SOURCE, "path");
    type(ID_NAME, "");
    expect(screen.getByLabelText(ID_SOURCE)).toHaveValue("path");
    type(ID_NAME, "data.id");
    await act(async () => fireEvent.click(screen.getByRole("button", { name: "선언 저장" })));
    expect(putCreate()).toMatchObject({ id_path: "data.id" });
    expect(putCreate()).not.toHaveProperty("id_field");
  });

  it("둘 다 비었을 때도 고른 출처를 유지하고, 비운 채면 그 칸에 문제를 보이며 저장하지 않는다", async () => {
    serve({ [GET_T]: [TARGETS], [GET_D("t1")]: [NONE] });
    mount();
    await pick("t1");
    await screen.findByText(/아직 선언이 없습니다/);
    fillValid();
    type(ID_NAME, "");
    type(ID_SOURCE, "field");
    expect(screen.getByLabelText(ID_SOURCE)).toHaveValue("field");
    expect(screen.getByText(/요청 본문의 ID 칸 중 하나를 적으세요/)).toHaveAttribute("role", "alert");
    expect(screen.getByRole("button", { name: "선언 저장" })).toBeDisabled();
    type(ID_SOURCE, "path");
    expect(screen.getByLabelText(ID_SOURCE)).toHaveValue("path");
    expect(api.mock.calls.some(([, init]) => init?.method === "PUT")).toBe(false);
  });

  it.each<[string, string, RegExp]>([
    ["표식 칸과 같음", "body", /표식 칸과 같거나 그 위·아래 칸이면 안 됩니다/],
    ["표식 칸 아래", "body.id", /표식 칸과 같거나 그 위·아래 칸이면 안 됩니다/],
    ["본문 틀에 이미 있음", "title", /본문 틀에 표식 칸이나 ID 칸이 이미 있습니다/],
  ])("ID 칸 규칙 위반(%s)은 문제를 보이고 저장하지 않는다", async (_name, value, text) => {
    serve({ [GET_T]: [TARGETS], [GET_D("t1")]: [NONE] });
    mount();
    await pick("t1");
    await screen.findByText(/아직 선언이 없습니다/);
    fillValid();
    type(ID_SOURCE, "field");
    type(ID_NAME, value);
    expect(screen.getByText(text)).toHaveAttribute("role", "alert");
    expect(screen.getByRole("button", { name: "선언 저장" })).toBeDisabled();
    expect(api.mock.calls.some(([, init]) => init?.method === "PUT")).toBe(false);
  });

  it("form 본문의 ID 칸에 점을 쓰면 그 칸에 문제를 보인다", async () => {
    serve({ [GET_T]: [TARGETS], [GET_D("t1")]: [NONE] });
    mount();
    await pick("t1");
    await screen.findByText(/아직 선언이 없습니다/);
    fillValid();
    type("자원 종류 1 본문 모양", "form");
    type("자원 종류 1 본문 틀", "title=t");
    type(ID_SOURCE, "field");
    type(ID_NAME, "a.b");
    expect(screen.getByText(/form 본문의 ID 칸에는 점/)).toHaveAttribute("role", "alert");
  });

  it("ID 칸에 자격 모양 값을 넣으면 이름만 알리고 원문은 문구에 없다", async () => {
    serve({ [GET_T]: [TARGETS], [GET_D("t1")]: [NONE] });
    mount();
    await pick("t1");
    await screen.findByText(/아직 선언이 없습니다/);
    fillValid();
    type(ID_SOURCE, "field");
    type(ID_NAME, "session_id=synthetic");
    expect(screen.getByRole("button", { name: "선언 저장" })).toBeDisabled();
    const alerts = screen
      .getAllByRole("alert")
      .map((a) => a.textContent ?? "")
      .join(" ");
    expect(alerts).toMatch(/자격 값 모양/);
    expect(alerts).not.toContain("synthetic");
  });

  it("서버가 create.id_field를 짚으면 ID 칸 옆에 알린다", async () => {
    serve({
      [GET_T]: [TARGETS],
      [GET_D("t1")]: [NONE],
      [PUT_D("t1")]: [reject(400, "invalid_declaration", "types[0].create.id_field")],
    });
    mount();
    await pick("t1");
    await screen.findByText(/아직 선언이 없습니다/);
    fillValid();
    type(ID_SOURCE, "field");
    type(ID_NAME, "title_id");
    await act(async () => fireEvent.click(screen.getByRole("button", { name: "선언 저장" })));
    const msg = await screen.findByText(/서버가 이 칸을 받지 않았습니다\(types\[0\]\.create\.id_field\)/);
    expect(msg.closest("fieldset")).toBe(screen.getByLabelText(ID_NAME).closest("fieldset"));
    expect(msg.parentElement).toContainElement(screen.getByLabelText(ID_NAME));
  });
});

describe("승인 범위는 단건 조회로(사용자 2026-11-22, 1차 리허설 F3)", () => {
  it("목록에 경로·메서드가 비어 와도 단건 조회 값으로 보이고, POST 승인 대상에 'POST 가 없습니다'를 띄우지 않는다", async () => {
    const stripped = { targets: TARGETS.targets.map((t) => ({ ...t, path_prefixes: undefined, methods: undefined })) };
    serve({ [GET_T]: [stripped], [GET_ONE("t1")]: [ONE("t1")], [GET_D("t1")]: [NONE] });
    mount();
    await pick("t1");
    await screen.findByText(/아직 선언이 없습니다/);
    expect(await screen.findByText("GET/POST")).toBeInTheDocument();
    fillValid();
    expect(screen.queryByText(/POST 가 없습니다/)).toBeNull();
  });

  it("단건 조회를 못 하면 경로·메서드를 '—'로 꾸미지 않고 경고도 만들지 않는다", async () => {
    const stripped = { targets: TARGETS.targets.map((t) => ({ ...t, path_prefixes: undefined, methods: undefined })) };
    serve({ [GET_T]: [stripped], [GET_ONE("t1")]: [reject(503)], [GET_D("t1")]: [NONE] });
    mount();
    await pick("t1");
    expect(await screen.findByText(/승인 경로·메서드를 읽지 못했습니다/)).toBeInTheDocument();
    fillValid();
    expect(screen.queryByText(/POST 가 없습니다/)).toBeNull();
  });
});
