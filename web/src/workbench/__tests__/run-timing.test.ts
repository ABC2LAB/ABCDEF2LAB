import { describe, expect, it } from "vitest";

import { parseURLJob, type URLDiagnosisJob } from "@/lib/diagnosis/diagnosis-url";

import { dateTimeOf, fmtDur } from "../live/map";
import {
  runDurationMissingWhy,
  runDurationSec,
  runElapsedSec,
  runStartMissingWhy,
  runStartText,
} from "../live/runTiming";
import { readFileSync } from "node:fs";
import { resolve } from "node:path";

// 실제 Go handler 출력과 대조된 공유 응답(docs/contracts/diagnosis-run-timing.responses.example.json)을 실 파서로 읽어 표시를 본다.
type Example = { cases: { name: string; status: { owner_context: string } }[] };
const example = JSON.parse(
  readFileSync(resolve(process.cwd(), "../docs/contracts/diagnosis-run-timing.responses.example.json"), "utf8"),
) as Example;
const job = (name: string): URLDiagnosisJob => {
  const c = example.cases.find((x) => x.name === name);
  if (!c) throw new Error(`사례 없음: ${name}`);
  return parseURLJob(c.status, c.status.owner_context);
};
const START = Date.parse("2026-11-17T01:02:03.123Z");

describe("점검 시작 · 경과 · 소요 표시 (diagnosis-run-timing.md 화면 인계)", () => {
  it("완료: 시작은 서버 시각, 소요는 finished_at − started_at(예시 5분), 경과는 세지 않는다", () => {
    const j = job("completed_with_times");
    expect(runStartText(j)).toBe(dateTimeOf(j.started_at));
    expect(runDurationSec(j)).toBe(300);
    expect(fmtDur(runDurationSec(j) as number)).toBe("5분");
    expect(runElapsedSec(j, START + 10 * 60_000)).toBeNull();
  });

  it("실행 중: 경과는 서버 started_at 기준(새로고침해도 0 이 아님), 소요는 세지 않는다", () => {
    const j = job("running_with_start");
    expect(runStartText(j)).toBe(dateTimeOf(j.started_at));
    expect(runElapsedSec(j, START + 90_000)).toBe(90);
    // 마운트 전(now 0)에는 세지 않고, 브라우저 시계가 서버보다 늦으면 0
    expect(runElapsedSec(j, 0)).toBeNull();
    expect(runElapsedSec(j, START - 5_000)).toBe(0);
    expect(runDurationSec(j)).toBeNull();
    expect(runDurationMissingWhy(j)).toMatch(/마감 시각이 아직 없습니다/);
  });

  it("결과 불명은 마감 시각이 없어도 실행 중으로 읽지 않는다(경과 없음)", () => {
    const j: URLDiagnosisJob = { ...job("running_with_start"), state: "unknown" };
    expect(runElapsedSec(j, START + 90_000)).toBeNull();
    expect(runStartText(j)).not.toBeNull(); // 확인된 시작 시각은 그대로 보인다
    // 소요 이유도 '아직 없음'(곧 생길 값)이 아니라 '남기지 않음'
    expect(runDurationMissingWhy(j)).toMatch(/^서버가 마감 시각을 남기지 않았습니다/);
    expect(runDurationMissingWhy({ ...j, state: "completed" })).toMatch(/^서버가 마감 시각을 남기지 않았습니다/);
    expect(runDurationMissingWhy({ ...j, state: "stopping" })).toMatch(/^마감 시각이 아직 없습니다/);
  });

  it("시작 전 · 시작 전 마감: 마감 시각 하나만으로 소요를 만들지 않고, 끝난 회차를 곧 시작될 것처럼 쓰지 않는다", () => {
    for (const [name, why] of [
      ["ready_without_times", "아직 엔진 실행이 시작되지 않았습니다"],
      ["failed_before_start_with_finish", "엔진 실행이 시작되지 않고 끝났습니다"],
    ] as const) {
      const j = job(name);
      expect(runStartText(j)).toBeNull();
      expect(runStartMissingWhy(j)).toBe(why);
      expect(runDurationSec(j)).toBeNull();
      expect(runElapsedSec(j, START + 90_000)).toBeNull();
    }
    const notStarted = job("ready_without_times");
    expect(runStartMissingWhy({ ...notStarted, state: "cancelled" })).toBe("엔진 실행이 시작되지 않고 끝났습니다");
    // 결과 불명은 시작을 확인하지 못했다는 뜻이라 '끝났다'고 단정하지 않는다
    expect(runStartMissingWhy({ ...notStarted, state: "unknown" })).toBe("엔진 실행 시작이 확인되지 않았습니다");
  });

  it("구 기록(사실은 true, 시각 없음): '이전 회차'로 알리고 추정하지 않는다", () => {
    const j = job("legacy_completed_without_times");
    expect(runStartText(j)).toBeNull();
    expect(runStartMissingWhy(j)).toBe("서버가 시작 시각을 남기지 않은 이전 회차입니다");
    expect(runDurationSec(j)).toBeNull();
    expect(runDurationMissingWhy(j)).toBe("시작·마감 시각이 둘 다 있을 때만 셉니다");
  });

  it("상태를 아직 못 읽었으면 값이 없다", () => {
    expect(runStartText(null)).toBeNull();
    expect(runDurationSec(undefined)).toBeNull();
    expect(runElapsedSec(null, START)).toBeNull();
    expect(runStartMissingWhy(null)).toBe("점검 상태를 아직 읽지 못했습니다");
  });
});
