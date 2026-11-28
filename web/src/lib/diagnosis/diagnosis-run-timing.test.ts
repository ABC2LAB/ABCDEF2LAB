import { describe, expect, it } from "vitest";

import { matchURLRun, parseURLRunPage, type URLRunSummary } from "./diagnosis-runs";
import { matchURLHandle, matchURLJob, parseURLJob, parseURLServerTime, type URLDiagnosisJob } from "./diagnosis-url";
import { envelope, handle, owner, ready, running } from "./diagnosis-url-fixtures";
import { readFileSync } from "node:fs";
import { resolve } from "node:path";

const startedAt = "2026-11-17T01:02:03.123456789Z";
const finishedAt = "2026-11-17T01:07:03.123456789Z";
const unknownJob: URLDiagnosisJob = { ...running, state: "unknown", phase: "closure", drain_complete: true };

function summary(job: URLDiagnosisJob): URLRunSummary {
  return {
    id: job.id,
    revision: job.revision,
    state: job.state,
    phase: job.phase,
    ...(job.task_id === undefined ? {} : { task_id: job.task_id }),
    started: job.started,
    drain_complete: job.drain_complete,
    result_ready: job.result_ready,
    ...(job.started_at === undefined ? {} : { started_at: job.started_at }),
    ...(job.finished_at === undefined ? {} : { finished_at: job.finished_at }),
    report_available: job.result_ready && !!job.task_id,
    approval_status: "not_revalidated",
    action_required: true,
  };
}

function page(run: unknown) {
  return {
    version: "diagnosis-api.v2",
    owner_context: owner,
    page: { runs: [run], snapshot: "d".repeat(64), limit: 25, more: false },
  };
}

function parsers(fields: Record<string, unknown>, job = unknownJob) {
  return [
    () => parseURLJob(envelope({ ...job, ...fields }), owner),
    () => parseURLRunPage(page({ ...summary(job), ...fields }), owner).runs[0],
  ];
}

describe("점검 시각의 상태·목록 공통 계약", () => {
  it("구 기록에서 생략된 시각을 채우지 않는다", () => {
    for (const parse of parsers({})) {
      const value = parse();
      expect(value).not.toHaveProperty("started_at");
      expect(value).not.toHaveProperty("finished_at");
    }
  });

  it.each([
    { started: false, drain_complete: false },
    { started: true, drain_complete: false, started_at: startedAt },
    { started: false, drain_complete: true, finished_at: finishedAt },
    { started: true, drain_complete: true, started_at: startedAt, finished_at: finishedAt },
    { started: true, drain_complete: true, finished_at: finishedAt },
  ])("결과 불명의 독립 확인 사실과 저장 시각을 보존한다: %j", (fields) => {
    for (const parse of parsers(fields)) expect(parse()).toMatchObject({ state: "unknown", ...fields });
  });

  it.each([
    { ...ready },
    { ...running, started_at: startedAt },
    { ...running, state: "completed" as const, drain_complete: true, result_ready: true },
    { ...running, state: "completed" as const, drain_complete: true, result_ready: true, finished_at: finishedAt },
    {
      ...ready,
      state: "failed" as const,
      preparation_started: true,
      drain_complete: true,
      finished_at: finishedAt,
    },
    {
      ...ready,
      state: "cancelled" as const,
      preparation_started: true,
      drain_complete: true,
      finished_at: finishedAt,
    },
  ])("구 완료·시작 전 마감도 기존 상태 규칙과 함께 받는다: $state", (job) => {
    for (const parse of parsers({}, job))
      expect(parse()).toMatchObject({
        state: job.state,
        started: job.started,
        drain_complete: job.drain_complete,
        result_ready: job.result_ready,
      });
  });

  it.each([
    "2026-11-17T01:02:03Z",
    "2026-11-17T01:02:03.1Z",
    "2026-11-17T01:02:03.000000001Z",
    startedAt,
    "2000-02-29T23:59:59.999999999Z",
    "2024-02-29T00:00:00Z",
    "0001-01-01T00:00:00.000000001Z",
    "0001-01-01T00:00:01Z",
    "9999-12-31T23:59:59.999999999Z",
  ])("UTC RFC3339Nano 시각 원문을 보존한다: %s", (at) => {
    for (const key of ["started_at", "finished_at"])
      for (const parse of parsers({ [key]: at })) expect(parse()).toHaveProperty(key, at);
  });

  it.each([
    null,
    undefined,
    "",
    0,
    {},
    [],
    true,
    "not-a-date",
    "2026-11-17",
    "2026-11-17T01:02:03+00:00",
    "2026-11-17T10:02:03+09:00",
    "2026-11-17t01:02:03z",
    "2026-11-17 01:02:03Z",
    "2026-11-17T01:02:03Z ",
    "2026-11-17T01:02:03.Z",
    "2026-11-17T01:02:03,1Z",
    "2026-11-17T01:02:03.1234567890Z",
    "0000-01-01T00:00:01Z",
    "10000-01-01T00:00:01Z",
    "0001-01-01T00:00:00Z",
    "0001-01-01T00:00:00.000000000Z",
    "2026-00-17T01:02:03Z",
    "2026-13-17T01:02:03Z",
    "2026-11-00T01:02:03Z",
    "2026-11-31T01:02:03Z",
    "2026-04-31T01:02:03Z",
    "2026-02-29T01:02:03Z",
    "2024-02-30T01:02:03Z",
    "1900-02-29T01:02:03Z",
    "2100-02-29T01:02:03Z",
    "2026-11-17T24:00:00Z",
    "2026-11-17T01:60:00Z",
    "2026-11-17T01:02:60Z",
  ])("잘못된 시각은 두 응답 모두 거절한다: %j", (at) => {
    for (const key of ["started_at", "finished_at"])
      for (const parse of parsers({ [key]: at })) expect(parse).toThrow();
  });

  it.each([
    { started: false, started_at: startedAt },
    { drain_complete: false, finished_at: finishedAt },
    { started_at: finishedAt, finished_at: startedAt },
    { started_at: "2026-11-17T01:02:03.123456790Z", finished_at: startedAt },
    { started_at: "2026-11-17T01:02:03.1Z", finished_at: "2026-11-17T01:02:03.099999999Z" },
    { started_at: startedAt, finished_at: finishedAt, elapsed_seconds: 300 },
  ])("사실 없는 시각·나노초 역행·미지원 필드는 거절한다: %j", (fields) => {
    for (const parse of parsers(fields)) expect(parse).toThrow();
  });

  it.each([
    { started_at: startedAt, finished_at: startedAt },
    { started_at: "2026-11-17T01:02:03.1Z", finished_at: "2026-11-17T01:02:03.1000Z" },
    { started_at: "2026-11-17T01:02:03.099999999Z", finished_at: "2026-11-17T01:02:03.1Z" },
  ])("같은 순간과 소수초 자리수가 다른 순서도 정확히 받는다: %j", (fields) => {
    for (const parse of parsers(fields)) expect(parse()).toMatchObject(fields);
  });
});

describe("시각의 정본 결속", () => {
  const timed: URLDiagnosisJob = {
    ...running,
    state: "stopping",
    phase: "closure",
    drain_complete: true,
    started_at: startedAt,
    finished_at: finishedAt,
  };

  it("새 확인 사실에 처음 기록된 시각을 받고 조회 handle도 원문을 보존한다", () => {
    const next = { ...running, started_at: startedAt };
    expect(matchURLJob(next, ready)).toBe(next);
    expect(matchURLRun(next, summary(ready))).toBe(next);
    expect(matchURLHandle(next, handle)).toBe(next);
    const draining = { ...timed, drain_complete: false, finished_at: undefined };
    const closed = { ...timed, revision: timed.revision + 1 };
    expect(matchURLJob(closed, draining)).toBe(closed);
    expect(matchURLRun(closed, summary(draining))).toBe(closed);
  });

  it.each(["started_at", "finished_at"] as const)("저장된 %s의 변경·삭제를 거절한다", (key) => {
    for (const at of [undefined, (key === "started_at" ? startedAt : finishedAt).replace("123456789Z", "123456790Z")])
      for (const revision of [timed.revision, timed.revision + 1]) {
        const next = { ...timed, revision, [key]: at };
        expect(() => matchURLJob(next, timed)).toThrow();
        expect(() => matchURLRun(next, summary(timed))).toThrow();
      }
  });

  it("서버 time.Equal처럼 소수초 자리수만 다른 같은 순간을 받는다", () => {
    const old = {
      ...timed,
      started_at: "2026-11-17T01:02:03.1Z",
      finished_at: "2026-11-17T01:07:03.1Z",
    };
    for (const revision of [old.revision, old.revision + 1]) {
      const next = {
        ...old,
        revision,
        started_at: "2026-11-17T01:02:03.100000000Z",
        finished_at: "2026-11-17T01:07:03.100000000Z",
      };
      expect(matchURLJob(next, old)).toBe(next);
      expect(matchURLRun(next, summary(old))).toBe(next);
    }
  });

  it.each(["started_at", "finished_at"] as const)("구 확인 사실의 누락 %s를 사후 보충하지 않는다", (key) => {
    const legacy = { ...timed, [key]: undefined };
    const next = { ...timed, revision: timed.revision + 1 };
    expect(() => matchURLJob(next, legacy)).toThrow();
    expect(() => matchURLRun(next, summary(legacy))).toThrow();
  });

  it("revision이 증가해도 기존 시각의 원문을 보존한다", () => {
    const next = { ...timed, revision: timed.revision + 1 };
    expect(matchURLJob(next, timed)).toBe(next);
    expect(matchURLRun(next, summary(timed))).toBe(next);
  });
});

type ContractExample = {
  version: string;
  cases: {
    name: string;
    status: { owner_context: string; draft: URLDiagnosisJob; server_time?: string };
    list: unknown;
  }[];
};
const example = JSON.parse(
  readFileSync(resolve(process.cwd(), "../docs/contracts/diagnosis-run-timing.responses.example.json"), "utf8"),
) as ContractExample;

describe("실제 Go handler와 공유하는 점검 시각 응답", () => {
  it("시작 전·실행 중·완료·시작 전 마감·구 기록·시간 상한 두 응답을 모두 확인한다", () => {
    expect(example.version).toBe("diagnosis-run-timing.examples.v1");
    expect(example.cases.map(({ name }) => name)).toEqual([
      "ready_without_times",
      "running_with_start",
      "completed_with_times",
      "failed_before_start_with_finish",
      "legacy_completed_without_times",
      "running_with_time_limit",
      "cancelled_keeps_time_limit",
    ]);
  });

  it.each(example.cases)("공유 원문을 두 닫힌 파서로 읽고 같은 회차에 결속한다: $name", ({ status, list }) => {
    const job = parseURLJob(status, status.owner_context);
    const run = parseURLRunPage(list, status.owner_context).runs[0];
    expect(job.started_at).toBe(status.draft.started_at);
    expect(job.finished_at).toBe(status.draft.finished_at);
    expect(run.started_at).toBe(job.started_at);
    expect(run.finished_at).toBe(job.finished_at);
    expect(Object.hasOwn(job, "started_at")).toBe(Object.hasOwn(status.draft, "started_at"));
    expect(Object.hasOwn(job, "finished_at")).toBe(Object.hasOwn(status.draft, "finished_at"));
    expect(Object.hasOwn(run, "started_at")).toBe(Object.hasOwn(job, "started_at"));
    expect(Object.hasOwn(run, "finished_at")).toBe(Object.hasOwn(job, "finished_at"));
    expect(matchURLRun(job, run)).toBe(job);
    // 시간 상한과 응답 시각은 상태 응답에만 있고, 함께 나타난다(목록 행에는 없다).
    expect(job.time_limit).toEqual(status.draft.time_limit);
    expect(parseURLServerTime(status, status.owner_context)).toBe(status.server_time);
    expect(Object.hasOwn(run, "time_limit")).toBe(false);
  });
});

describe("시간 상한(§시간 상한)", () => {
  const found = example.cases.find(({ name }) => name === "running_with_time_limit");
  if (!found) throw new Error("shared time limit example missing");
  const limited = found.status;
  const parse = (draft: Record<string, unknown>, extra: Record<string, unknown> = {}) =>
    parseURLJob({ ...limited, ...extra, draft: { ...limited.draft, ...draft } }, limited.owner_context);

  it("남은 시간은 서버 응답 시각 기준으로 센다(브라우저 시계 아님)", () => {
    const job = parse({});
    const server = String(parseURLServerTime(limited, limited.owner_context));
    const limit = job.time_limit;
    if (!limit) throw new Error("time limit not parsed");
    expect(Date.parse(limit.run_deadline_at) - Date.parse(server)).toBe(417_000);
    expect(Date.parse(limit.run_deadline_at) - Date.parse(limit.model_close_at)).toBe(230_000);
  });

  it("같은 revision의 새 server_time은 Job 정본 비교를 바꾸지 않는다", () => {
    const current = parse({});
    const polled = { ...limited, server_time: "2026-11-17T01:05:04Z" };
    expect(parseURLServerTime(polled, limited.owner_context)).toBe("2026-11-17T01:05:04Z");
    const next = parseURLJob(polled, limited.owner_context);
    expect(matchURLJob(next, current)).toBe(next);
    expect(next).toEqual(current);
  });

  it("같은 revision의 같은 시간 상한은 소수 초 표기가 달라도 같은 순간으로 본다", () => {
    const current = parse({});
    if (!current.time_limit) throw new Error("time limit not parsed");
    const next = parse({
      time_limit: {
        ...current.time_limit,
        run_deadline_at: "2026-11-17T01:12:00.0Z",
        model_close_at: "2026-11-17T01:08:10.000000000Z",
      },
    });
    expect(matchURLJob(next, current)).toBe(next);
  });

  it.each([
    { name: "식이 맞지 않음", limit: { shutdown_margin_ms: 20_001 } },
    { name: "음수 몫", limit: { model_drain_ms: -1, shutdown_margin_ms: 20_001 + 89_999 } },
    { name: "정수가 아님", limit: { model_drain_ms: 90_000.5 } },
    { name: "모르는 칸", limit: { extra: 1 } },
    { name: "offset 시각", limit: { run_deadline_at: "2026-11-17T10:12:00+09:00" } },
    { name: "null", limit: null },
  ])("모양이 틀린 시간 상한을 거부한다: $name", ({ limit }) => {
    const value = limit === null ? null : { ...limited.draft.time_limit, ...limit };
    expect(() => parse({ time_limit: value })).toThrow();
  });

  it("server_time은 시간 상한과 함께만 있다", () => {
    expect(() => parse({}, { server_time: undefined })).toThrow();
    expect(() => parse({}, { server_time: "2026-11-17T10:05:03+09:00" })).toThrow();
    const { time_limit: _, ...noLimit } = limited.draft;
    expect(() => parseURLJob({ ...limited, draft: noLimit }, limited.owner_context)).toThrow();
    expect(() => parse({ preparation_started: false, started: false, task_id: undefined })).toThrow();
  });

  it("한 번 받은 시간 상한은 지우거나 바꾸지 않고, 시작 뒤 사후 보충도 받지 않는다", () => {
    const job = parse({});
    const { time_limit: _, ...withoutLimit } = job;
    const later = { ...job, revision: job.revision + 1 };
    expect(matchURLJob(later, job)).toBe(later);
    expect(() => matchURLJob({ ...withoutLimit, revision: job.revision + 1 }, job)).toThrow();
    if (!job.time_limit) throw new Error("time limit not parsed");
    const moved = {
      ...job.time_limit,
      run_deadline_at: "2026-11-17T01:13:00Z",
      model_close_at: "2026-11-17T01:09:10Z",
    };
    expect(() => matchURLJob({ ...later, time_limit: moved }, job)).toThrow();
    expect(() => matchURLJob(later, withoutLimit)).toThrow();
  });
});
