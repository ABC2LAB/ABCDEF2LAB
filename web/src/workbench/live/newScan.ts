// live 새 점검 폼 계산 (순수 함수). 엔진이 받는 칸만: 이름 · 설명(대상 URL 포함) · 목표 · LLM 프로필 · 제외 경로 · 커버리지.
// 주소 해석·내부 주소 판단은 시안 lib/newScan 의 parseUrl · isInternal 을 그대로 쓴다(허용 목록·소유 확인은 엔진 미지원이라 안 씀).

import { isInternal, parseUrl } from "../lib/newScan";
import { type CreateTaskInput, excludePathRules, type LlmProfile } from "./api";

/** live 새 점검 폼 값. */
export interface LiveScanForm {
  url: string;
  name: string;
  /** 이름을 사람이 고쳤는가 (고치기 전에는 'hostname 점검' 으로 채운다) */
  nameTouched: boolean;
  goal: string;
  /** 고른 프로필 id. null = 아직 안 고름(기본 프로필), "" = 보내지 않음(엔진 활성 설정) */
  profile: string | null;
  /** 제외 경로 (줄마다 하나) */
  excl: string;
  coverage: boolean;
}

/** 처음 값 (주소 비움). */
export function initialLiveForm(): LiveScanForm {
  return { url: "", name: "", nameTouched: false, goal: "", profile: null, excl: "", coverage: true };
}

/** 이름을 안 고쳤고 주소가 맞으면 'hostname 점검'. 바뀐 게 없으면 같은 객체. */
export function syncLiveName(f: LiveScanForm): LiveScanForm {
  if (f.nameTouched) return f;
  const u = parseUrl(f.url);
  const name = u ? `${u.hostname} 점검` : "";
  return name === f.name ? f : { ...f, name };
}

/** 프로필 기본값: is_default 인 것, 없으면 "" (엔진 활성 설정). */
export function defaultProfileId(profiles: readonly LlmProfile[] | null): string {
  return profiles?.find((p) => p.is_default)?.id ?? "";
}

/** 제외 경로 글 → 줄 목록 (빈 줄 뺌). */
export function exclLines(text: string): string[] {
  return text
    .split("\n")
    .map((x) => x.trim())
    .filter(Boolean);
}

/** 가운데 `*` 가 있는 줄 (부분 일치라 끝의 `*` 만 지워 주고, 가운데 `*` 는 글자 그대로 비교된다). */
export function badExcl(lines: readonly string[]): string[] {
  return lines.filter((l) => l.replace(/\*+$/, "").includes("*"));
}

/** 주소 상태: 오류 글 또는 해석한 URL · 내부 주소 여부. */
export type LiveTarget =
  | { ok: false; error: string; url?: undefined; internal?: undefined; target?: undefined }
  | { ok: true; error?: undefined; url: URL; internal: boolean; target: string };

/** 주소 칸 → 대상. http(s) 만, user:pass@ 금지. target 은 설명에 넣을 글(경로가 없으면 origin). */
export function liveTarget(raw: string): LiveTarget {
  const filled = !!raw.trim();
  const u = parseUrl(raw);
  if (!u) {
    return {
      ok: false,
      error: filled ? "주소 형식을 확인하세요. 예: https://shop.example.com" : "점검할 사이트 주소를 입력하세요.",
    };
  }
  if (u.protocol !== "http:" && u.protocol !== "https:")
    return { ok: false, error: "http 또는 https 주소만 점검할 수 있습니다." };
  if (u.username || u.password) return { ok: false, error: "주소에 계정 정보(user:pass@)를 넣지 마세요." };
  const path = u.pathname === "/" ? "" : u.pathname;
  return { ok: true, url: u, internal: isInternal(u.hostname), target: `${u.origin}${path}${u.search}` };
}

/** 주소 칸을 떠날 때: 스킴 없이 적은 맞는 주소면 https:// 를 붙인 대상 글(경로 유지). 바꿀 게 없으면 null. */
export function normalizeLiveUrl(raw: string): string | null {
  if (/^[a-z][a-z0-9+.-]*:\/\//i.test(raw.trim())) return null;
  const t = liveTarget(raw);
  return t.ok ? t.target : null;
}

/** 설명 글: 엔진은 description+goal 안 첫 URL 을 대상으로 잡는다 → 대상 URL 을 꼭 넣는다. */
export function describeTarget(target: string): string {
  return `대상 ${target}`;
}

/** 폼 계산 결과. */
export interface LiveScanView {
  target: LiveTarget;
  nameOk: boolean;
  excl: string[];
  badExcl: string[];
  /** 고른 프로필 id ("" = 보내지 않음) */
  profileId: string;
  /** 시작할 수 없는 까닭 (비면 시작 가능) */
  why: string[];
  /** 지금 값으로 만든 입력 (요약 표시용, 대상이 틀리면 설명이 빈다) */
  draft: CreateTaskInput;
  /** createTask 입력 (시작할 수 없으면 null) */
  input: CreateTaskInput | null;
}

/** 폼 값 + 프로필 목록 → 화면 값과 보낼 입력. profiles 가 null(미지원·불러오는 중)이면 프로필을 보내지 않는다. */
export function liveScanView(f: LiveScanForm, profiles: readonly LlmProfile[] | null): LiveScanView {
  const target = liveTarget(f.url);
  const name = f.name.trim();
  const excl = exclLines(f.excl);
  const bad = badExcl(excl);
  const known = profiles ?? [];
  let profileId = f.profile ?? defaultProfileId(profiles);
  // 목록에 없는 id(지워진 프로필 · 미지원)는 보내지 않는다
  if (profileId && !known.some((p) => p.id === profileId)) profileId = "";

  const why: string[] = [];
  if (!target.ok) why.push(target.error);
  if (!name) why.push("작업 이름을 입력하세요");
  if (bad.length) why.push("제외 경로의 * 는 끝에만 쓸 수 있습니다");

  const draft: CreateTaskInput = {
    name,
    description: target.ok ? describeTarget(target.target) : "",
    goal: f.goal.trim(),
    llm_profile_ids: profileId ? [profileId] : [],
    coverage_enabled: f.coverage,
    intercept_rules: excludePathRules(excl),
  };
  const input = target.ok && why.length === 0 ? draft : null;
  return { target, nameOk: !!name, excl, badExcl: bad, profileId, why, draft, input };
}

/** 화면 요약용: createTask 가 실제로 보내는 본문 모양 (빈 칸은 createTask 처럼 뺀다). 비밀 값은 없다. */
export function requestPreview(input: CreateTaskInput): Record<string, unknown> {
  const body: Record<string, unknown> = { name: input.name, description: input.description, goal: input.goal };
  const ids = (input.llm_profile_ids ?? []).map((v) => Number(v));
  if (ids.length) body.llm_profile_ids = ids;
  if (input.coverage_enabled !== undefined) body.coverage_enabled = input.coverage_enabled;
  if (input.intercept_rules?.length) body.intercept_rules = input.intercept_rules;
  return body;
}
