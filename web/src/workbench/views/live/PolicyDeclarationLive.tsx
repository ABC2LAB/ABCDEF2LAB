"use client";

// 정책 선언 화면 (운영자 전용). 승인된 대상 하나에 자원 종류별 접근 정책(공유·소유자 전용, 시험 객체 생성·조회 요청 모양)을
//   선언한다. E③ 확정 단계는 실행 시작 때 이 선언의 동결본을 읽는다(#698 §3, #596 §4).
// 데이터·규칙은 workbench/live/declaration seam 에서만 온다. 화면은 그리기만(표현/데이터 분리).
// ※ 저장 허용·동결은 서버가 정한다. 화면 검사는 미리 보여 주는 것뿐이고, 승인 범위 경고는 참고다(실제 허용은 E²).
// ※ 절대규칙 2: 자격 값은 받지도 보내지도 않는다. 본문 틀에 자격으로 보이는 칸이 있으면 저장을 막는다. 내보내기·공유 없음.
// ※ 디자인은 윤재상 기준으로 맞출 뼈대다 — 기존 workbench 클래스·토큰만 쓴다.

import { type ReactNode, useEffect, useMemo, useRef, useState } from "react";

import { cx } from "../../lib/cx";
import {
  type AccountRoles,
  BODY_KIND_LABEL,
  type BodyKind,
  CREATE_METHODS,
  type CreateMethod,
  confirmable,
  type DeclarationDraft,
  type DeclarationLoad,
  type DeclarationTarget,
  type DraftIssue,
  draftFieldOf,
  draftFromStored,
  emptyDraft,
  emptyType,
  getDeclaration,
  getDeclarationTargetScope,
  getDeclarationTargets,
  invalidatedReasonText,
  normalizeDraft,
  type RoleReadiness,
  rejectedFieldOf,
  roleReadiness,
  SAVE_FAILURE_TEXT,
  type SaveFailure,
  SHARING_LABEL,
  type Sharing,
  type StoredDeclaration,
  saveDeclaration,
  saveFailureOf,
  scopeWarnings,
  type TypeDraft,
  validateDraft,
} from "../../live/declaration";
import { useShell } from "../../shell";
import { useWb } from "../../store";
import { Icon } from "../../ui/icons";
import s from "../../workbench.module.css";
import { BackButton, UrlHead } from "./urlParts";

/** 한 번 읽은 선언. key = 그 요청을 시작한 대상 + 진입 세대 + 다시 읽기 횟수 — 지금 요청의 결과만 그린다. */
type Read = { key: string; load: DeclarationLoad | "failed" };

/** 칸 하나: 이름 + 입력 + 그 칸의 문제(오류는 빨강, 승인 범위 경고는 참고). */
function Field({
  label,
  issues,
  warnings,
  help,
  children,
}: {
  label: string;
  issues: string[];
  warnings?: string[];
  help?: string;
  children: ReactNode;
}) {
  return (
    <div className={s.field}>
      <span className={s.lbl}>{label}</span>
      {children}
      {help ? <span className={s.help}>{help}</span> : null}
      {issues.map((m) => (
        <span key={m} className={s.err} role="alert">
          {m}
        </span>
      ))}
      {(warnings ?? []).map((m) => (
        <span key={m} className={s.help} data-warn="">
          ⚠ {m}
        </span>
      ))}
    </div>
  );
}

/** 문제 목록에서 한 칸의 문구만. */
const at = (list: DraftIssue[], field: string): string[] => list.filter((i) => i.field === field).map((i) => i.message);
const splitFields = (v: string): string[] =>
  v
    .split(/[,\s]+/)
    .map((x) => x.trim())
    .filter(Boolean);

/** 종류의 시험 여부 배지 — 소유자 전용 + 표식 칸이 소유자 전용 필드일 때만 확정 단계 시험 대상. */
function TypeBadge({ t }: { t: TypeDraft }) {
  if (t.sharing !== "owner_only") return <span className={cx(s.tag, s.ex)}>공유 — 시험하지 않음(건너뜀)</span>;
  if (!confirmable(t)) return <span className={cx(s.tag, s.ap)}>표식 칸 미지정 — 시험 불가</span>;
  return <span className={cx(s.tag, s.in)}>확정 단계 시험 대상</span>;
}

/** 객체 ID 출처(e3contract.CreateSpec): 생성 응답 JSON 경로(id_path) 또는 생성 요청 본문 칸(id_field) 중 하나. */
type IdSource = "path" | "field";
const ID_SOURCE_LABEL: Record<IdSource, string> = { path: "응답 경로", field: "요청 본문 칸" };
const ID_SOURCE_HELP: Record<IdSource, string> = {
  path: "서버가 ID를 정하는 API — 생성 응답 JSON 경로(예: id, data.id).",
  field:
    "클라이언트가 ID를 정하는 API — 확정 단계가 새 난수 ID를 이 본문 칸과 조회 경로의 {id}에 넣습니다. json은 점 경로, form은 점 없는 키. 본문 틀에는 이 칸을 넣지 않습니다. ID는 비밀이 아니라 결과 · 쓰기 기록에 남습니다.",
};
function idSourceOf(c: TypeDraft["create"], picked: IdSource): IdSource {
  if (c.idField !== "") return "field";
  if (c.idPath !== "") return "path";
  return picked;
}

/** 자원 종류 하나의 입력. */
function TypeEditor({
  t,
  index,
  issues,
  warnings,
  onChange,
  onRemove,
}: {
  t: TypeDraft;
  index: number;
  issues: DraftIssue[];
  warnings: DraftIssue[];
  onChange: (next: TypeDraft) => void;
  onRemove: () => void;
}) {
  const f = (name: string) => at(issues, `types.${index}.${name}`);
  const w = (name: string) => at(warnings, `types.${index}.${name}`);
  const setCreate = (patch: Partial<TypeDraft["create"]>) => onChange({ ...t, create: { ...t.create, ...patch } });
  const setRead = (patch: Partial<TypeDraft["read"]>) => onChange({ ...t, read: { ...t.read, ...patch } });
  const owner = t.sharing === "owner_only";
  // 객체 ID 출처는 채워진 칸이 정한다. 둘 다 비었을 때만 운영자가 고른 쪽을 쓴다(선언에는 출처 칸이 따로 없다)
  const [picked, setPicked] = useState<IdSource>(() => idSourceOf(t.create, "path"));
  // 채워진 칸(저장본에서 온 것이든 운영자가 친 것이든)의 출처를 고른 쪽으로 기억한다 — 칸 이름을 다 지웠다가 다시 쳐도 출처가
  // '응답 경로'로 돌아가 id_field 이름이 id_path 로 들어가지 않게(#775 윤재상 리뷰). 렌더 중에 맞춰 같은 렌더에서 쓴다
  const filled: IdSource | null =
    t.create.idField !== "" || t.create.idPath !== "" ? idSourceOf(t.create, picked) : null;
  if (filled && filled !== picked) setPicked(filled);
  const idSource = idSourceOf(t.create, filled ?? picked);
  // 출처를 바꾸면 적던 이름을 옮긴다 — 두 칸이 같이 남지 않게(서버는 둘 다 있으면 거부)
  const pickIdSource = (next: IdSource) => {
    setPicked(next);
    if (next === idSource) return;
    const name = idSource === "path" ? t.create.idPath : t.create.idField;
    setCreate(next === "path" ? { idPath: name, idField: "" } : { idPath: "", idField: name });
  };
  return (
    <fieldset className={s.isec} aria-label={`자원 종류 ${index + 1}`} data-type-index={index}>
      <div className={s.now}>
        <b>자원 종류 {index + 1}</b>
        <TypeBadge t={t} />
        <button className={cx(s.btn, s.sm)} type="button" onClick={onRemove}>
          종류 빼기
        </button>
      </div>
      <Field label="자원 종류 이름" issues={f("type")} help="예: note, order (resource_ref 접두가 됩니다)">
        <input
          className={cx(s.inp, s.mono, s["w-sel"])}
          aria-label={`자원 종류 ${index + 1} 이름`}
          value={t.type}
          onChange={(e) => onChange({ ...t, type: e.target.value })}
        />
      </Field>
      <Field label="공유 방식" issues={f("sharing")}>
        <select
          className={cx(s.inp, s["w-sel"])}
          aria-label={`자원 종류 ${index + 1} 공유 방식`}
          value={t.sharing}
          onChange={(e) => onChange({ ...t, sharing: e.target.value as Sharing })}
        >
          {(Object.keys(SHARING_LABEL) as Sharing[]).map((k) => (
            <option key={k} value={k}>
              {SHARING_LABEL[k]}
            </option>
          ))}
        </select>
      </Field>
      {owner ? (
        <>
          <Field
            label="소유자 전용 필드"
            issues={f("ownerOnlyFields")}
            help="소유자만 읽어야 하는 응답 JSON 경로. 쉼표로 나눕니다(예: body, detail.memo)."
          >
            <input
              className={cx(s.inp, s.mono, s["w-url"])}
              aria-label={`자원 종류 ${index + 1} 소유자 전용 필드`}
              value={t.ownerOnlyFields.join(", ")}
              onChange={(e) => onChange({ ...t, ownerOnlyFields: splitFields(e.target.value) })}
            />
          </Field>
          <h3>시험 객체 생성</h3>
          <Field label="생성 경로" issues={f("create.path")} warnings={w("create.path")}>
            <input
              className={cx(s.inp, s.mono, s["w-path"])}
              aria-label={`자원 종류 ${index + 1} 생성 경로`}
              value={t.create.path}
              onChange={(e) => setCreate({ path: e.target.value })}
              placeholder="/api/things"
            />
          </Field>
          <Field label="생성 메서드" issues={f("create.method")} warnings={w("create.method")}>
            <select
              className={cx(s.inp, s["w-sel"])}
              aria-label={`자원 종류 ${index + 1} 생성 메서드`}
              value={t.create.method}
              onChange={(e) => setCreate({ method: e.target.value as CreateMethod })}
            >
              {CREATE_METHODS.map((m) => (
                <option key={m} value={m}>
                  {m}
                </option>
              ))}
            </select>
          </Field>
          <Field label="본문 모양" issues={f("create.bodyKind")}>
            <select
              className={cx(s.inp, s["w-sel"])}
              aria-label={`자원 종류 ${index + 1} 본문 모양`}
              value={t.create.bodyKind}
              onChange={(e) => setCreate({ bodyKind: e.target.value as BodyKind })}
            >
              {(Object.keys(BODY_KIND_LABEL) as BodyKind[]).map((k) => (
                <option key={k} value={k}>
                  {BODY_KIND_LABEL[k]}
                </option>
              ))}
            </select>
          </Field>
          <Field
            label="본문 틀"
            issues={f("create.bodyTemplate")}
            help="표식 칸 · 요청 본문 ID 칸은 비워 두세요(프로그램이 넣습니다). 비밀번호 · 토큰 · 쿠키 · 세션 값은 넣지 않습니다."
          >
            <textarea
              className={cx(s.inp, s.mono)}
              style={{ height: "auto", minHeight: 72, padding: "8px 12px" }}
              aria-label={`자원 종류 ${index + 1} 본문 틀`}
              value={t.create.bodyTemplate}
              onChange={(e) => setCreate({ bodyTemplate: e.target.value })}
            />
          </Field>
          <Field label="표식 칸" issues={f("create.markerField")} help="소유자 전용 필드 중 하나">
            <select
              className={cx(s.inp, s.mono, s["w-sel"])}
              aria-label={`자원 종류 ${index + 1} 표식 칸`}
              value={t.create.markerField}
              onChange={(e) => setCreate({ markerField: e.target.value })}
            >
              <option value="">표식 칸 선택…</option>
              {t.ownerOnlyFields.map((x) => (
                <option key={x} value={x}>
                  {x}
                </option>
              ))}
            </select>
          </Field>
          <Field
            label={idSource === "path" ? "객체 ID — 생성 응답 경로" : "객체 ID — 요청 본문 칸"}
            issues={[...f("create.idPath"), ...f("create.idField")]}
            help={ID_SOURCE_HELP[idSource]}
          >
            <div className={s.now}>
              <select
                className={cx(s.inp, s["w-sel"])}
                aria-label={`자원 종류 ${index + 1} 객체 ID 출처`}
                value={idSource}
                onChange={(e) => pickIdSource(e.target.value as IdSource)}
              >
                {(Object.keys(ID_SOURCE_LABEL) as IdSource[]).map((k) => (
                  <option key={k} value={k}>
                    {ID_SOURCE_LABEL[k]}
                  </option>
                ))}
              </select>
              <input
                className={cx(s.inp, s.mono, s["w-sel"])}
                aria-label={`자원 종류 ${index + 1} 객체 ID 위치`}
                value={idSource === "path" ? t.create.idPath : t.create.idField}
                onChange={(e) =>
                  setCreate(idSource === "path" ? { idPath: e.target.value } : { idField: e.target.value })
                }
                placeholder={idSource === "path" ? "id" : "title"}
              />
            </div>
          </Field>
          <h3>객체 조회</h3>
          <Field label="조회 경로 틀" issues={f("read.pathTemplate")} warnings={w("read.pathTemplate")}>
            <input
              className={cx(s.inp, s.mono, s["w-path"])}
              aria-label={`자원 종류 ${index + 1} 조회 경로 틀`}
              value={t.read.pathTemplate}
              onChange={(e) => setRead({ pathTemplate: e.target.value })}
              placeholder="/api/things/{id}"
            />
          </Field>
          <Field label="응답 속 데이터 위치" issues={f("read.dataPath")} help="본문 전체면 .">
            <input
              className={cx(s.inp, s.mono, s["w-sel"])}
              aria-label={`자원 종류 ${index + 1} 데이터 위치`}
              value={t.read.dataPath}
              onChange={(e) => setRead({ dataPath: e.target.value })}
            />
          </Field>
          <Field
            label="상태 칸 · 정상 값 (선택)"
            issues={f("read.statusPath")}
            help="둘 다 비우면 정상 응답은 전송 통제의 HTTP 상태 판정을 따릅니다. 확정은 어느 쪽이든 표식 일치가 필요합니다."
          >
            <div className={s.now}>
              <input
                className={cx(s.inp, s.mono, s["w-sel"])}
                aria-label={`자원 종류 ${index + 1} 상태 칸`}
                value={t.read.statusPath}
                onChange={(e) => setRead({ statusPath: e.target.value })}
                placeholder="status"
              />
              <input
                className={cx(s.inp, s.mono, s["w-sel"])}
                aria-label={`자원 종류 ${index + 1} 정상 값`}
                value={t.read.normalValue}
                onChange={(e) => setRead({ normalValue: e.target.value })}
                placeholder="success"
              />
            </div>
          </Field>
        </>
      ) : (
        <p className={s.help}>
          공유 종류는 확정 단계가 요청을 보내지 않고 판정 행도 만들지 않습니다(건너뜀으로 보입니다).
        </p>
      )}
    </fieldset>
  );
}

/** 계정 역할 상태 문구(계약 §4). 확정 단계는 역할 없음·등급 확인 없음이면 꺼진다. 인증 결과는 실행 때 서버가 따로 본다. */
const ROLE_READINESS_TEXT: Record<RoleReadiness, string> = {
  undeclared: "확정 단계 꺼짐 — 계정 역할을 정하지 않았습니다",
  privilege_unconfirmed: "확정 단계 꺼짐 — 두 계정이 같은 권한 등급이라는 확인이 없습니다",
  declared: "계정 역할 정함 — 두 계정이 실행 때 인증되어야 확정 단계가 돕니다",
};

/** 저장된 선언 요약(서버 값). */
function StoredSummary({ d }: { d: StoredDeclaration }) {
  return (
    <>
      <p className={s.help}>
        선언자 <span className={s.mono}>{d.declaredBy}</span> · 선언 시각 <span className={s.mono}>{d.declaredAt}</span>{" "}
        · 대상 승인 판 <span className={s.mono}>{d.targetApprovalRevision}</span> · 자원 종류 {d.types.length}개(시험
        대상 {d.types.filter(confirmable).length}개)
      </p>
      <p className={s.help}>
        {d.roles ? (
          <>
            소유 · 생성 계정 <span className={s.mono}>{d.roles.ownerAccountLabel}</span> · 교차 조회 계정{" "}
            <span className={s.mono}>{d.roles.crossAccountLabel}</span> ·{" "}
          </>
        ) : null}
        {ROLE_READINESS_TEXT[roleReadiness(d.roles)]}
      </p>
    </>
  );
}

const NO_ROLES: AccountRoles = { ownerAccountLabel: "", crossAccountLabel: "", accountsSamePrivilege: false };

/**
 * 확정 단계 계정 역할 입력(계약 §4, 선택 칸). 서버가 계정을 이름순 등으로 고르면 관리자가 교차 조회자가 되어 거짓 확정이
 * 날 수 있어서, 운영자가 두 계정과 같은 권한 등급임을 정한다. 라벨은 등록된 시험 계정의 이름이고 자격 값이 아니다.
 */
function RolesEditor({
  roles,
  issues,
  onChange,
}: {
  roles: AccountRoles | undefined;
  issues: DraftIssue[];
  onChange: (next: AccountRoles) => void;
}) {
  const r = roles ?? NO_ROLES;
  return (
    <>
      <h3>확정 단계 시험 계정 (선택)</h3>
      <p className={s.help}>
        객체를 만드는 계정(A)과 그 객체를 교차 조회하는 계정(B)을 정하고, 두 계정이 같은 권한 등급임을 확인해야 확정
        단계가 돕니다. 관리자처럼 남의 객체를 정상 권한으로 읽는 계정을 B로 두면 결함이 없어도 확정이 날 수 있습니다.
      </p>
      <Field
        label="소유 · 생성 계정 (A)"
        issues={at(issues, "ownerAccountLabel")}
        help="등록된 시험 계정 이름(예: tester-a)"
      >
        <input
          className={cx(s.inp, s.mono, s["w-sel"])}
          aria-label="소유 · 생성 계정"
          value={r.ownerAccountLabel}
          onChange={(e) => onChange({ ...r, ownerAccountLabel: e.target.value })}
        />
      </Field>
      <Field
        label="교차 조회 계정 (B)"
        issues={at(issues, "crossAccountLabel")}
        help="A 와 다른 시험 계정, 같은 권한 등급"
      >
        <input
          className={cx(s.inp, s.mono, s["w-sel"])}
          aria-label="교차 조회 계정"
          value={r.crossAccountLabel}
          onChange={(e) => onChange({ ...r, crossAccountLabel: e.target.value })}
        />
      </Field>
      <Field label="권한 등급 확인" issues={at(issues, "accountsSamePrivilege")}>
        <label className={s.ckline}>
          <input
            type="checkbox"
            aria-label="두 계정이 같은 권한 등급"
            checked={r.accountsSamePrivilege}
            onChange={(e) => onChange({ ...r, accountsSamePrivilege: e.target.checked })}
          />
          <span>두 계정은 같은 권한 등급입니다(예: 둘 다 일반 사용자, 관리자 아님)</span>
        </label>
      </Field>
      <p className={s.help} data-role-readiness={roleReadiness(roles)}>
        {ROLE_READINESS_TEXT[roleReadiness(roles)]}
      </p>
    </>
  );
}

/** 정책 선언 (운영자 전용). */
export function PolicyDeclarationLive() {
  const { route, goTo } = useShell();
  const visible = route === "declaration";
  const [targets, setTargets] = useState<DeclarationTarget[] | "failed" | null>(null);
  const [targetId, setTargetId] = useState("");
  const listed = Array.isArray(targets) ? (targets.find((t) => t.id === targetId) ?? null) : null;
  // 승인 경로·메서드는 단건 조회로 채운다(목록 응답은 비움, 1차 리허설 F3). 같은 판일 때만 합친다
  const [scope, setScope] = useState<{
    key: string;
    value: Awaited<ReturnType<typeof getDeclarationTargetScope>> | "failed";
  } | null>(null);
  const [attempt, setAttempt] = useState(0);
  const scopeKey = visible && listed ? `${listed.id}|${listed.revision}` : "";
  const scopeNow = scope && scope.key === scopeKey ? scope.value : null;
  // 합친 대상은 같은 입력이면 같은 객체여야 한다 — 아래 선언 조회 effect가 대상 객체에 걸려 있어, 렌더마다 새 객체면 다시
  // 읽기를 끝없이 되풀이한다
  const target = useMemo(
    () =>
      listed && scopeNow && scopeNow !== "failed" && scopeNow.revision === listed.revision
        ? { ...listed, pathPrefixes: scopeNow.pathPrefixes, methods: scopeNow.methods, scopeKnown: true }
        : listed,
    [listed, scopeNow],
  );
  // 진입 세대: 화면에 다시 들어올 때마다 올린다. 렌더 중에 맞춰서 같은 렌더에서 지난 진입의 결과를 버린다
  const [entry, setEntry] = useState({ visible, gen: 0 });
  if (entry.visible !== visible) setEntry({ visible, gen: visible ? entry.gen + 1 : entry.gen });
  const request = visible && target ? `${target.id}|${target.revision}|${entry.gen}|${attempt}` : "";
  const [read, setRead] = useState<Read | null>(null);
  const [draft, setDraft] = useState<DeclarationDraft>(emptyDraft);
  const [touched, setTouched] = useState(false);
  const [busy, setBusy] = useState(false);
  const [saveError, setSaveError] = useState<SaveFailure | null>(null);
  // 서버가 저장을 거부하며 짚은 칸(400 invalid_declaration 의 field). 값은 오지 않는다 — 칸 이름만
  const [serverField, setServerField] = useState<string | null>(null);
  // 저장 결과 문구: clean = 저장한 내용 그대로, newer = 저장을 기다리는 동안 더 고친 입력이 있어 그 입력을 남김
  const [saved, setSaved] = useState<"" | "clean" | "newer">("");
  // 지금 화면 대상. 저장 응답은 저장을 시작한 대상이 아직 화면 대상일 때만 반영한다(대상을 바꾼 뒤 늦게 온 응답은 버림)
  const currentTarget = useRef("");
  currentTarget.current = targetId;
  // 저장 성공 뒤 다시 읽은 서버 값으로 입력을 맞춘다(손댄 입력이어도) — 저장을 누른 뒤 더 고치지 않았을 때만
  const reseedAfterSave = useRef(false);
  // 입력 세대: 고칠 때마다 올린다. 저장 응답이 올 때 저장 시작 세대와 다르면 그 사이 고친 입력을 서버 값으로 덮지 않는다
  const editGen = useRef(0);

  // 승인된 대상 목록(화면에 들어올 때마다 다시 읽는다 — 그사이 재승인·철회를 놓치지 않게)
  // biome-ignore lint/correctness/useExhaustiveDependencies: entry.gen 은 진입마다 다시 읽는 트리거
  useEffect(() => {
    if (!visible) return;
    const controller = new AbortController();
    let live = true;
    getDeclarationTargets(controller.signal)
      .then((t) => {
        if (live) setTargets(t);
      })
      .catch(() => {
        if (live) setTargets("failed");
      });
    return () => {
      live = false;
      controller.abort();
    };
  }, [visible, entry.gen]);

  useEffect(() => {
    if (!scopeKey || !listed) return;
    const controller = new AbortController();
    let live = true;
    getDeclarationTargetScope(listed.id, controller.signal)
      .then((value) => {
        if (live) setScope({ key: scopeKey, value: value ?? "failed" });
      })
      .catch(() => {
        if (live) setScope({ key: scopeKey, value: "failed" });
      });
    return () => {
      live = false;
      controller.abort();
    };
  }, [scopeKey, listed]);

  useEffect(() => {
    if (!request || !target) return;
    const controller = new AbortController();
    let live = true;
    getDeclaration(target.id, controller.signal)
      .then((load) => {
        if (live) setRead({ key: request, load });
      })
      .catch(() => {
        if (live) setRead({ key: request, load: "failed" });
      });
    return () => {
      live = false;
      controller.abort();
    };
  }, [request, target]);

  const now = read && request && read.key === request ? read.load : null;
  const stored =
    now && now !== "failed" && (now.state === "current" || now.state === "invalidated") ? now.declaration : null;

  // 새 조회 결과가 오면 입력을 서버 값으로 맞춘다 — 대상이 바뀌었거나, 아직 손대지 않았거나, 방금 저장했을 때만.
  // 다시 들어와 같은 대상을 다시 읽었을 때 저장 안 한 입력을 덮지 않는다. 무효·없음·준비 전·깨짐은 빈 선언에서 시작
  const [seeded, setSeeded] = useState({ key: "", target: "" });
  if (now && read && seeded.key !== read.key && target) {
    const reseed = seeded.target !== target.id || !touched || reseedAfterSave.current;
    setSeeded({ key: read.key, target: target.id });
    if (reseed) {
      reseedAfterSave.current = false;
      setDraft(now !== "failed" && now.state === "current" ? draftFromStored(now.declaration) : emptyDraft());
      setTouched(false);
    }
  }

  const serverIssue: DraftIssue[] = serverField
    ? [{ field: draftFieldOf(serverField) ?? "", message: `서버가 이 칸을 받지 않았습니다(${serverField}).` }]
    : [];
  const issues = [...(touched ? validateDraft(draft) : []), ...serverIssue];
  const allIssues = validateDraft(draft);
  const warnings = target ? scopeWarnings(draft, target) : [];
  // 기존 선언을 확인한 경우(없음·유효·무효)에만 입력을 연다. 조회 실패·모양 깨짐·라우트 없음·대상 없음에서 빈 입력을
  // 열면 기존 내용을 모른 채 전체 교체 PUT 이 나갈 수 있다
  const canEdit =
    !!target &&
    now !== null &&
    now !== "failed" &&
    (now.state === "none" || now.state === "current" || now.state === "invalidated");

  const pickTarget = (id: string) => {
    setTargetId(id);
    setSaveError(null);
    setServerField(null);
    setSaved("");
  };
  // 새 점검에서 '정책 선언'으로 오면 그 대상을 미리 고른다(사용자 11/23). 한 번 온 요청은 한 번만 쓴다
  const focus = useWb((st) => st.focusTarget);
  const usedFocus = useRef(0);
  // biome-ignore lint/correctness/useExhaustiveDependencies: pickTarget 은 렌더마다 새로 만들지만 하는 일이 같다
  useEffect(() => {
    if (!visible || !Array.isArray(targets) || !focus || focus.seq === usedFocus.current) return;
    usedFocus.current = focus.seq;
    if (targets.some((t) => t.id === focus.id)) pickTarget(focus.id);
  }, [visible, targets, focus]);
  const update = (next: DeclarationDraft) => {
    editGen.current++;
    reseedAfterSave.current = false;
    setDraft(next);
    setTouched(true);
    setSaved("");
    setSaveError(null);
    setServerField(null);
  };
  const save = () => {
    if (!target || busy) return;
    setTouched(true);
    if (allIssues.length) return;
    const forTarget = target.id;
    const startGen = editGen.current;
    setBusy(true);
    setSaveError(null);
    setServerField(null);
    saveDeclaration(target, normalizeDraft(draft))
      .then(() => {
        if (forTarget !== currentTarget.current) return;
        // 로컬 값을 믿지 않고 서버 값을 다시 읽어 보인다. 기다리는 동안 더 고쳤으면 그 입력은 덮지 않고 남긴다
        const clean = editGen.current === startGen;
        setSaved(clean ? "clean" : "newer");
        reseedAfterSave.current = clean;
        setAttempt((n) => n + 1);
      })
      .catch((e: unknown) => {
        if (forTarget !== currentTarget.current) return;
        setSaveError(saveFailureOf(e));
        setServerField(rejectedFieldOf(e));
      })
      .finally(() => setBusy(false));
  };

  return (
    <>
      <UrlHead title="정책 선언" crumb="정책 선언" acts={<BackButton to="새 점검" onClick={() => goTo("new")} />} />
      <div className={s.vb}>
        <div className={s.vm}>
          <div className={s.note} role="note">
            <Icon name="info" />
            <span>
              대상의 자원 종류마다 누가 읽어도 되는지(공유 · 소유자 전용)와 시험 객체를 만들고 읽는 요청 모양을
              선언합니다. 확정 단계는 점검 시작 때 이 선언을 고정해 읽고, 프로그램이 소유자 전용 종류마다 시험 객체를
              만듭니다. 대상을 다시 승인하면 선언은 무효가 됩니다. 자격 값은 넣지 않습니다.
            </span>
          </div>

          <section className={s.isec} aria-label="대상 선택">
            <h2>대상 선택</h2>
            {targets === "failed" ? <p className={s.empty}>대상 목록을 불러오지 못했습니다.</p> : null}
            {targets === null ? <p className={s.empty}>대상 목록 불러오는 중…</p> : null}
            {Array.isArray(targets) && targets.length === 0 ? (
              <p className={s.empty}>승인된 대상이 없습니다. 대상을 먼저 등록 · 승인하세요.</p>
            ) : null}
            {Array.isArray(targets) && targets.length > 0 ? (
              <select
                className={cx(s.inp, s["w-url"])}
                aria-label="선언할 대상"
                value={targetId}
                onChange={(e) => pickTarget(e.target.value)}
              >
                <option value="">대상 선택…</option>
                {targets.map((t) => (
                  <option key={t.id} value={t.id}>
                    {t.origin}
                  </option>
                ))}
              </select>
            ) : null}
            {target?.scopeKnown ? (
              <p className={s.help}>
                승인 경로 <span className={s.mono}>{target.pathPrefixes.join(", ") || "—"}</span> · 승인 메서드{" "}
                <span className={s.mono}>{target.methods.join("/") || "—"}</span> · 승인 판{" "}
                <span className={s.mono}>{target.revision}</span>
              </p>
            ) : null}
            {target && !target.scopeKnown ? (
              <p className={s.help}>
                {scopeNow === "failed"
                  ? "승인 경로·메서드를 읽지 못했습니다 — 승인 범위 경고를 계산하지 않습니다."
                  : "승인 경로·메서드를 읽는 중…"}{" "}
                · 승인 판 <span className={s.mono}>{target.revision}</span>
              </p>
            ) : null}
          </section>

          {target ? (
            <section className={s.isec} aria-label="현재 선언">
              <h2>현재 선언</h2>
              {now === null ? <p className={s.empty}>불러오는 중…</p> : null}
              {now === "failed" ? (
                <div className={cx(s.note, s.warn)} role="alert">
                  <Icon name="alert" />
                  <span className={s["lv-grow"]}>선언을 불러오지 못했습니다. 선언이 없다는 뜻이 아닙니다.</span>
                  <button className={cx(s.btn, s.sm)} type="button" onClick={() => setAttempt((n) => n + 1)}>
                    다시 읽기
                  </button>
                </div>
              ) : null}
              {now !== null && now !== "failed" && now.state === "unavailable" ? (
                <div className={cx(s.note, s.warn)} role="status">
                  <Icon name="alert" />
                  <span>서버에 선언 저장 기능이 아직 없습니다. 선언이 없다는 뜻이 아닙니다.</span>
                </div>
              ) : null}
              {now !== null && now !== "failed" && now.state === "not_found" ? (
                <div className={cx(s.note, s.warn)} role="alert">
                  <Icon name="alert" />
                  <span>이 대상을 찾지 못했습니다(삭제됐거나 다른 운영자의 대상). 선언이 없다는 뜻이 아닙니다.</span>
                </div>
              ) : null}
              {now !== null && now !== "failed" && now.state === "invalid" ? (
                <div className={cx(s.note, s.warn)} role="alert">
                  <Icon name="alert" />
                  <span className={s["lv-grow"]}>
                    저장된 선언의 모양을 확인할 수 없어 표시하지 않습니다(fail-closed). 기존 선언을 확인하기 전에는
                    입력을 열지 않습니다.
                  </span>
                  <button className={cx(s.btn, s.sm)} type="button" onClick={() => setAttempt((n) => n + 1)}>
                    다시 읽기
                  </button>
                </div>
              ) : null}
              {now !== null && now !== "failed" && now.state === "none" ? (
                <p className={s.empty}>이 대상에는 아직 선언이 없습니다. 선언이 없으면 확정 단계는 돌지 않습니다.</p>
              ) : null}
              {now !== null && now !== "failed" && now.state === "invalidated" ? (
                <div className={cx(s.note, s.warn)} role="status">
                  <Icon name="alert" />
                  <span>{invalidatedReasonText(now.reason)} — 다시 선언해야 확정 단계가 돕니다.</span>
                </div>
              ) : null}
              {stored ? <StoredSummary d={stored} /> : null}
            </section>
          ) : null}

          {canEdit ? (
            <section className={s.isec} aria-label="선언 입력">
              <h2>선언 입력</h2>
              <h3>신원 확인 (대상마다 하나)</h3>
              <Field
                label="신원 확인 경로 (GET)"
                issues={at(issues, "identity.path")}
                warnings={at(warnings, "identity.path")}
                help="로그인한 계정의 내 정보 조회 등"
              >
                <input
                  className={cx(s.inp, s.mono, s["w-path"])}
                  aria-label="신원 확인 경로"
                  value={draft.identity.path}
                  onChange={(e) => update({ ...draft, identity: { ...draft.identity, path: e.target.value } })}
                  placeholder="/api/me"
                />
              </Field>
              <Field
                label="응답 속 사용자 식별자 위치"
                issues={at(issues, "identity.subjectIdPath")}
                help="응답 JSON 경로(예: id, data.user.id)"
              >
                <input
                  className={cx(s.inp, s.mono, s["w-sel"])}
                  aria-label="사용자 식별자 위치"
                  value={draft.identity.subjectIdPath}
                  onChange={(e) => update({ ...draft, identity: { ...draft.identity, subjectIdPath: e.target.value } })}
                />
              </Field>
              <RolesEditor roles={draft.roles} issues={issues} onChange={(roles) => update({ ...draft, roles })} />
              {at(issues, "types").map((m) => (
                <p key={m} className={s.err} role="alert">
                  {m}
                </p>
              ))}
              {draft.types.map((t, i) => (
                <TypeEditor
                  // biome-ignore lint/suspicious/noArrayIndexKey: 종류는 이름이 비거나 겹칠 수 있어 자리로 구분한다
                  key={i}
                  t={t}
                  index={i}
                  issues={issues}
                  warnings={warnings}
                  onChange={(next) => update({ ...draft, types: draft.types.map((x, j) => (j === i ? next : x)) })}
                  onRemove={() => update({ ...draft, types: draft.types.filter((_, j) => j !== i) })}
                />
              ))}
              <div className={s.now}>
                <button
                  className={cx(s.btn, s.sm)}
                  type="button"
                  onClick={() => update({ ...draft, types: [...draft.types, emptyType()] })}
                >
                  자원 종류 추가
                </button>
                <button
                  className={cx(s.btn, s.pri)}
                  type="button"
                  disabled={busy || (touched && allIssues.length > 0)}
                  onClick={save}
                >
                  {busy ? "저장 중…" : "선언 저장"}
                </button>
              </div>
              {warnings.length ? (
                <p className={s.help}>
                  승인 범위 경고는 참고입니다 — 저장은 막지 않고, 실제 전송은 확정 단계에서 전송 통제가 요청마다 다시
                  봅니다.
                </p>
              ) : null}
              {saveError ? (
                <p className={s.err} role="alert">
                  {SAVE_FAILURE_TEXT[saveError]}
                  {serverField ? (
                    <>
                      {" "}
                      문제 칸: <span className={s.mono}>{serverField}</span>
                    </>
                  ) : null}
                </p>
              ) : null}
              {saved === "clean" && !saveError ? (
                <p className={s.help} role="status">
                  저장했습니다. 서버에 저장된 선언을 다시 읽어 보입니다.
                </p>
              ) : null}
              {saved === "newer" && !saveError ? (
                <p className={s.help} role="status">
                  저장했습니다. 저장을 누른 뒤 고친 입력은 그대로 두었고 아직 저장 전입니다.
                </p>
              ) : null}
            </section>
          ) : null}
        </div>
      </div>
    </>
  );
}
