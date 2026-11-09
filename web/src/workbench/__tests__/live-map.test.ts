// live/map 순수 함수: 엔진 DTO → 화면 값.

import { describe, expect, it } from "vitest";

import { ApiError } from "@/abc2lab-ui/lib/api";

import type { LiveActivity, LiveTask } from "../live/api";
import {
  approvalCounts,
  clockOf,
  elapsed,
  errorText,
  feedRow,
  findingStatusLabel,
  fmtCount,
  fmtDur,
  fmtTokens,
  groupOf,
  hostOf,
  isActive,
  isLocalBaseUrl,
  isOffline,
  isUnsupported,
  kindOf,
  parseIntentSummary,
  pathOf,
  planState,
  relTime,
  sevKey,
  taskLabel,
  taskPhase,
  urlOf,
} from "../live/map";

const T = (p: Partial<LiveTask> = {}) => ({ id: "7", name: "", description: "", goal: "", ...p });

describe("hostOf · urlOf · taskLabel", () => {
  it("description → goal → name 순서로 첫 URL 의 hostname", () => {
    expect(hostOf(T({ description: "점검 대상: https://shop.example.com/login 입니다." }))).toBe("shop.example.com");
    expect(hostOf(T({ goal: "http://10.0.0.5:8080/api 의 IDOR" }))).toBe("10.0.0.5");
    expect(hostOf(T({ name: "x https://a.test, y" }))).toBe("a.test");
    expect(hostOf(T({ description: "https://first.test", goal: "https://second.test" }))).toBe("first.test");
    expect(urlOf(T({ description: "대상(https://b.test/x)." }))).toBe("https://b.test/x");
  });

  it("URL 이 없으면 null, 이름표는 name → description → 점검 #id", () => {
    expect(hostOf(T({ description: "주문 API 점검" }))).toBeNull();
    expect(taskLabel(T({ name: "주문 점검", description: "no url" }))).toBe("주문 점검");
    expect(taskLabel(T({ description: "짧은 설명" }))).toBe("짧은 설명");
    expect(taskLabel(T())).toBe("점검 #7");
    expect(taskLabel(T({ name: "이름", goal: "https://g.test" }))).toBe("g.test");
  });
});

describe("taskPhase · isActive · elapsed", () => {
  it("상태별 글과 색", () => {
    expect(taskPhase({ status: "running" })).toEqual({ label: "점검 중", tone: "run" });
    expect(taskPhase({ status: "created" }).tone).toBe("wait");
    expect(taskPhase({ status: "queued" })).toEqual({ label: "대기열", tone: "wait" });
    expect(taskPhase({ status: "paused" })).toEqual({ label: "일시정지됨", tone: "wait" });
    expect(taskPhase({ status: "done" })).toEqual({ label: "점검 완료", tone: "ok" });
    expect(taskPhase({ status: "failed" })).toEqual({ label: "실패", tone: "stop" });
    expect(taskPhase({ status: "timeout" })).toEqual({ label: "시간 초과", tone: "stop" });
    expect(taskPhase({ status: "weird" })).toEqual({ label: "weird", tone: "stop" });
  });

  it("끝난 상태만 isActive=false", () => {
    for (const st of ["created", "queued", "running", "paused"]) expect(isActive({ status: st })).toBe(true);
    for (const st of ["done", "failed", "timeout"]) expect(isActive({ status: st })).toBe(false);
  });

  it("경과: 끝났으면 완료 시각까지, 아니면 지금까지", () => {
    expect(elapsed({ created_unix: 1000, completed_unix: 1600 }, 9_999_000)).toBe(600);
    expect(elapsed({ created_unix: 1000, completed_unix: 0 }, 1_720_000)).toBe(720);
    expect(elapsed({ created_unix: 0, completed_unix: 0 }, 1_720_000)).toBe(0);
    expect(fmtDur(40)).toBe("40초");
    expect(fmtDur(720)).toBe("12분");
    expect(fmtDur(3900)).toBe("1시간 5분");
    expect(fmtDur(7200)).toBe("2시간");
  });
});

describe("시각 · 수", () => {
  const now = new Date(2026, 10, 9, 15, 0, 0).getTime();
  it("relTime: 방금 · 분 전 · 시간 전 · 날짜", () => {
    expect(relTime(now - 20_000, now)).toBe("방금");
    expect(relTime(now - 12 * 60_000, now)).toBe("12분 전");
    expect(relTime(new Date(now - 3 * 3_600_000).toISOString(), now)).toBe("3시간 전");
    expect(relTime(new Date(2026, 9, 23, 9, 0).getTime(), now)).toBe("10/23");
    expect(relTime(new Date(2025, 2, 5).getTime(), now)).toBe("2025/03/05");
    // unix 초도 받는다
    expect(relTime(Math.floor((now - 5 * 60_000) / 1000), now)).toBe("5분 전");
    expect(relTime("", now)).toBe("");
    expect(relTime(0, now)).toBe("");
    expect(relTime("nope", now)).toBe("");
  });

  it("clockOf 는 로컬 HH:MM:SS", () => {
    const d = new Date(2026, 10, 9, 7, 5, 9);
    expect(clockOf(d.toISOString())).toBe("07:05:09");
  });

  it("fmtCount · fmtTokens", () => {
    expect(fmtCount(1234567)).toBe("1,234,567");
    expect(fmtTokens(4_200_000)).toEqual({ n: "4.2", unit: "M" });
    expect(fmtTokens(830_000)).toEqual({ n: "830", unit: "K" });
    expect(fmtTokens(12_300)).toEqual({ n: "12.3", unit: "K" });
    expect(fmtTokens(512)).toEqual({ n: "512", unit: "" });
  });
});

describe("발견", () => {
  it("심각도 → Sev 키, 모르는 값은 null", () => {
    expect(sevKey("critical")).toBe("crit");
    expect(sevKey("high")).toBe("hi");
    expect(sevKey("MEDIUM")).toBe("mid");
    expect(sevKey("low")).toBe("lo");
    expect(sevKey("info")).toBeNull();
  });

  it("처리 상태 이름은 판정 낱말(확정·의심·판단 불가)을 쓰지 않는다", () => {
    const all = [
      "pending",
      "in_progress",
      "confirmed",
      "resolved",
      "fixed",
      "false_positive",
      "ignored",
      "duplicate",
      "risk_accepted",
    ];
    for (const st of all) {
      const label = findingStatusLabel(st);
      expect(label).not.toBe(st);
      expect(label).not.toMatch(/확정|의심|판단 불가/);
    }
    expect(findingStatusLabel("new_state")).toBe("new_state");
  });
});

describe("경로", () => {
  it("pathOf 는 스킴·호스트·쿼리를 떼고 {param} 은 둔다", () => {
    expect(pathOf("https://shop.example.com/api/orders/{id}?x=1#h")).toBe("/api/orders/{id}");
    expect(pathOf("http://h:8080")).toBe("/");
    expect(pathOf("/login?next=/")).toBe("/login");
    expect(pathOf("api/a")).toBe("/api/a");
    expect(pathOf("")).toBe("/");
  });

  it("groupOf 는 첫 경로 조각", () => {
    expect(groupOf("/api/orders/1")).toBe("/api");
    expect(groupOf("https://x.test/admin")).toBe("/admin");
    expect(groupOf("/")).toBe("/");
  });

  it("kindOf 는 lib/kind 규칙 (추정)", () => {
    expect(kindOf("get", "https://x.test/api/orders/1")).toBe("get");
    expect(kindOf("POST", "/api/login")).toBe("login");
    expect(kindOf("POST", "/api/cart")).toBe("ap");
    expect(kindOf("GET", "/admin/delete/1")).toBe("ex");
    expect(kindOf("", "/x")).toBe("ap");
  });
});

describe("의도 · 활동", () => {
  it("planState", () => {
    expect(planState("open")).toEqual({ row: "wait", label: "대기" });
    expect(planState("running")).toEqual({ row: "run", label: "진행 중" });
    expect(planState("done")).toEqual({ row: "done", label: "완료" });
    expect(planState("blocked")).toEqual({ row: "other", label: "blocked" });
  });

  it("parseIntentSummary: asset_ids · 예전 키 · 깨진 JSON", () => {
    expect(parseIntentSummary('{"summary":"주문 IDOR 확인","asset_ids":[12,"13"]}')).toEqual({
      summary: "주문 IDOR 확인",
      assetIds: [12, 13],
    });
    expect(parseIntentSummary('{"summary":"x","target_id":5}').assetIds).toEqual([5]);
    expect(parseIntentSummary('{"text":"y","target_ids":[1]}')).toEqual({ summary: "y", assetIds: [1] });
    expect(parseIntentSummary("not json")).toEqual({ summary: "not json", assetIds: [] });
    expect(parseIntentSummary("null")).toEqual({ summary: "", assetIds: [] });
  });

  it("feedRow: 종류 이름 · 점 색 · 빈 요약은 도구 이름", () => {
    const base: LiveActivity = {
      seq: 3,
      intent_id: "9",
      worker: "w1",
      ts: new Date(2026, 10, 9, 10, 1, 2).toISOString(),
      kind: "tool_use",
      tool: "http_request",
      is_error: false,
      summary: "",
    };
    expect(feedRow(base)).toEqual({
      key: "3",
      time: "10:01:02",
      kindLabel: "도구 호출",
      text: "http_request 호출",
      isError: false,
      dot: "col",
    });
    expect(feedRow({ ...base, kind: "result", summary: "200 OK" }).dot).toBe("ok");
    expect(feedRow({ ...base, kind: "result", is_error: true, summary: "실패" })).toMatchObject({
      dot: "blk",
      isError: true,
    });
    expect(feedRow({ ...base, kind: "llm_switch", summary: "모델 바꿈" })).toMatchObject({
      kindLabel: "모델 전환",
      dot: null,
    });
  });
});

describe("전송 통제 · 모델 · 오류", () => {
  it("approvalCounts: 허용 · 막음(denied+unsupported) · 보류(pending) · 불명", () => {
    const run = {
      execution: {
        counts: {
          attempts: 9,
          allowed: 4,
          denied: 2,
          unsupported: 1,
          forward_attempted: 4,
          responses: 4,
          unknown: 1,
          calls: 9,
        },
      },
    } as unknown as Parameters<typeof approvalCounts>[0];
    expect(approvalCounts(run, [{ state: "pending" }, { state: "allowed" }, { state: "pending" }])).toEqual({
      allowed: 4,
      blocked: 3,
      held: 2,
      unknown: 1,
      known: true,
    });
    expect(approvalCounts(null, null)).toEqual({ allowed: 0, blocked: 0, held: 0, unknown: 0, known: false });
  });

  it("isLocalBaseUrl (추정)", () => {
    for (const u of [
      "http://localhost:11434/v1",
      "http://127.0.0.1:1234",
      "http://[::1]:8000",
      "http://host.docker.internal:1234/v1",
      "http://192.168.0.10:8080",
      "http://10.1.2.3",
      "http://172.20.0.2",
    ]) {
      expect(isLocalBaseUrl(u)).toBe(true);
    }
    expect(isLocalBaseUrl("https://api.openai.com/v1")).toBe(false);
    expect(isLocalBaseUrl("https://api.example.com/?ollama=1")).toBe(false);
    expect(isLocalBaseUrl("")).toBe(false);
  });

  it("isUnsupported · isOffline · errorText", () => {
    for (const st of [403, 404, 409, 501, 503]) expect(isUnsupported(new ApiError(st, "x"))).toBe(true);
    for (const st of [0, 400, 401, 500]) expect(isUnsupported(new ApiError(st, "x"))).toBe(false);
    expect(isUnsupported(new Error("x"))).toBe(false);

    expect(isOffline(new ApiError(0, "x"))).toBe(true);
    expect(isOffline(new ApiError(502, "x"))).toBe(true);
    expect(isOffline(new ApiError(500, "HTTP 500"))).toBe(true);
    expect(isOffline(new ApiError(500, "db down"))).toBe(false);
    expect(isOffline(new TypeError("Failed to fetch"))).toBe(true);

    expect(errorText(new ApiError(0, "x"))).toBe("엔진에 연결하지 못했습니다.");
    expect(errorText(new ApiError(403, "x"))).toBe("이 서버 모드에서는 제공되지 않음");
    expect(errorText(new ApiError(401, "x"))).toBe("로그인이 필요합니다.");
    expect(errorText(new ApiError(400, "bad"))).toBe("엔진 오류 (HTTP 400): bad");
  });
});
