"use client";

// 새 점검 (시안 짜임: 대상 · 분석 모델 · 작업 + 오른쪽 점검 요약 · 안전 정책 · 시작).
// 순서: 입력 → [승인 범위 확인](초안 + 서버 범위 확인, DNS·HTTP·모델 호출 없음) → 서버가 돌려준 범위·예산을 오른쪽에서 보고
// 동의 상자 → [점검 시작]. 시작 전에는 task · Planner · 대상 송신 · 단회 권위를 만들지 않는다(서버 계약).
// URL 은 요청일 뿐이고 허가는 서버 설치 범위와 E② 가 정한다. 계정 로그인 · 점검 목표 · 범위 · 워커 수는 서버가 받지 않아 두지 않는다.

import { type ReactNode, useEffect, useRef, useState } from "react";

import { type URLDiagnosisInput, urlDiagnosisInput, type WorkflowMode } from "@/lib/diagnosis/diagnosis-url";

import { cx } from "../../lib/cx";
import { disconnectUrl, runUrlAction, setUrlConfirmed, useUrl, useUrlGates } from "../../live/urlDiagnosis";
import { scanViewFor, URL_ENDED_STATES, urlHost, urlTargetHint, usdText } from "../../live/urlMap";
import { useShell } from "../../shell";
import { useWb } from "../../store";
import { Icon } from "../../ui/icons";
import { CloseInspButton, InspOpenButton, PaneToggle } from "../../ui/parts";
import s from "../../workbench.module.css";
import { ExternalModelSettings } from "./ExternalModelSettings";
import { TargetApprovalPanel } from "./TargetApprovalPanel";
import { reasonText, UrlHead, UrlNotices, UrlPill } from "./urlParts";

/** 입력 → 서버에 보낼 초안 입력 (팀 lib 검사를 통과해야만). */
function draftInput(
  target: string,
  profile: string,
  name: string,
  mode?: WorkflowMode,
  excludes = "",
): URLDiagnosisInput | null {
  if (!profile) return null;
  try {
    return urlDiagnosisInput(target, profile, name, mode, excludes);
  } catch {
    return null;
  }
}

const MODES = [
  ["user_approval_v1", "사용자 승인", "탐색 뒤 점검할 경로를 고르고, 데이터를 바꾸는 요청은 한 건씩 승인합니다"],
  ["ai_auto_v1", "AI 자동", "승인 범위 안에서 탐색과 점검을 이어 갑니다 · 데이터 변경은 자동 승인 안 함"],
] as const;

/** 안전 정책 한 줄 (시안 ilist). */
function Rule({ t, sub }: { t: ReactNode; sub?: string }) {
  return (
    <li>
      <Icon name="check" />
      <div>
        {t}
        {sub !== undefined ? <small>{sub}</small> : null}
      </div>
    </li>
  );
}

/** live 새 점검 (URL 진단). */
export function UrlNewScanLive() {
  const { goTo } = useShell();
  const cap = useUrl((st) => st.cap);
  const handle = useUrl((st) => st.handle);
  const job = useUrl((st) => st.job);
  const flowStage = useUrl((st) => st.flow?.stage ?? null);
  const verified = useUrl((st) => st.verified);
  const busy = useUrl((st) => st.busy);
  const confirmed = useUrl((st) => st.confirmed);
  const resetSeq = useUrl((st) => st.resetSeq);
  const g = useUrlGates();
  const prefill = useWb((st) => st.prefill);
  const profiles = cap.kind === "v2" ? cap.catalog.profiles : [];
  const locals = profiles.filter((p) => !p.model?.external_transfer);
  const externals = profiles.filter((p) => p.model?.external_transfer);

  const [target, setTarget] = useState("");
  const [kind, setKind] = useState<"local" | "external">(locals.length || !externals.length ? "local" : "external");
  const [profileId, setProfileId] = useState("");
  const [name, setName] = useState("");
  const [workflowMode, setWorkflowMode] = useState<WorkflowMode>("user_approval_v1");
  const [excluded, setExcluded] = useState("");
  const [credentialReady, setCredentialReady] = useState(false);
  const seenSeq = useRef(useWb.getState().prefill?.seq ?? 0);
  const seenReset = useRef(resetSeq);

  // '다른 진단 만들기' 뒤에는 폼을 비운다 (같은 입력으로 겹쳐 만들지 않게)
  useEffect(() => {
    if (resetSeq === seenReset.current) return;
    seenReset.current = resetSeq;
    setTarget("");
    setName("");
    setExcluded("");
  }, [resetSeq]);

  // 다른 화면의 '이 주소로 다시 점검' (이어 보는 진단이 없을 때만)
  useEffect(() => {
    if (!prefill || prefill.seq === seenSeq.current) return;
    seenSeq.current = prefill.seq;
    if (!useUrl.getState().handle) setTarget(prefill.url);
  }, [prefill]);

  // 조회 중인 진단이 있으면 그 입력을 보인다 (잠금)
  const shown = handle?.input;
  const choices = kind === "external" ? externals : locals;
  const pickedId = choices.some((p) => p.id === profileId) ? profileId : (choices[0]?.id ?? "");
  const pv = shown ? shown.profile_id : pickedId;
  const profile = profiles.find((p) => p.id === pv) ?? null;
  const tv = shown ? shown.target_url : target;
  const nv = shown ? (shown.display_name ?? "") : name;
  const mv = shown?.workflow_mode ?? workflowMode;
  const ev = shown ? (shown.excluded_paths ?? "") : excluded;
  const external = !!profile?.model?.external_transfer;
  const lockedKind = external ? "external" : "local";
  const shownKind = shown ? lockedKind : kind;
  const input = shown
    ? null
    : draftInput(target, pv, name, profile?.workflow ? workflowMode : undefined, profile?.workflow ? excluded : "");

  const hint = urlTargetHint(tv);
  const why: string[] = [];
  if (!shown) {
    if (!target.trim()) why.push("주소를 입력하세요");
    else if (hint) why.push(hint);
    else if (!input && profile) why.push("주소 형식을 확인하세요");
    if (!profiles.length) why.push("설치된 모델 프로필이 없습니다");
    else if (!profile) why.push("모델을 고르세요");
    else if (profile.workflow && !name.trim()) why.push("작업 이름을 입력하세요");
    if (external && !credentialReady) why.push("외부 API 키를 등록하세요");
  }
  const canSend =
    !shown &&
    !!input &&
    g.canValidate &&
    (!profile?.workflow || !!name.trim()) &&
    !busy &&
    (!external || credentialReady);
  const send = async () => {
    if (canSend && input) await runUrlAction("validate", input);
  };
  const start = async () => {
    if (job && (await runUrlAction("start"))) goTo(job.input.workflow_mode ? "crawl" : "live");
  };

  const excludedCount = ev.split("\n").filter((l) => l.trim()).length;
  const scope = job?.scope;
  const to = job ? scanViewFor(job, flowStage) : null;
  const started = !!job && job.state !== "ready" && job.state !== "awaiting_approval";
  const ended = !!job && URL_ENDED_STATES.includes(job.state);

  // 오른쪽 아래 실행 단계 (입력 → 범위 확인 → 동의 · 시작)
  let action: ReactNode;
  if (!handle) {
    action = (
      <>
        <button className={cx(s.btn, s.pri, s.full)} type="button" disabled={!canSend} onClick={() => void send()}>
          {busy ? "확인 중…" : "승인 범위 확인"}
        </button>
        <span className={s.why}>{why.join(" · ")}</span>
      </>
    );
  } else if (!job) {
    action = <p className={s.help}>{busy ? "진단 상태를 확인하는 중…" : "진단 상태를 확인하지 못했습니다."}</p>;
  } else if (job.state === "awaiting_approval") {
    action = (
      <>
        <div className={cx(s.note, s.warn)} role="note">
          <Icon name="alert" />
          <span>
            {job.reason_code
              ? `아직 시작할 수 없습니다 · ${reasonText(job.reason_code)}`
              : "설치자 승인이 필요합니다. 입력이나 확인 상자만으로 실행 권한이 생기지 않습니다."}
          </span>
        </div>
        <button
          className={cx(s.btn, s.full)}
          type="button"
          disabled={!g.canValidate}
          onClick={() => void runUrlAction("validate")}
        >
          승인 범위 다시 확인
        </button>
      </>
    );
  } else if (job.state === "ready") {
    action = (
      <>
        {scope?.approval_mode === "confirm_every_request" ? (
          <div className={cx(s.note, s.warn)} role="note">
            <Icon name="alert" />
            <span>요청마다 확인하는 프로필은 작업대에서 시작할 수 없습니다.</span>
          </div>
        ) : null}
        <label className={s.ckline}>
          <input
            type="checkbox"
            checked={confirmed}
            disabled={!g.canStart}
            onChange={(e) => setUrlConfirmed(e.target.checked)}
          />
          <span>
            {scope?.model?.external_transfer
              ? "대상 응답·도구 출력의 외부 전송과 비용 상한에 동의하고, 위 범위로 점검을 시작합니다."
              : "위 범위로 점검을 시작합니다."}
          </span>
        </label>
        <button
          className={cx(s.btn, s.pri, s.full)}
          type="button"
          disabled={!g.canStart || !confirmed}
          onClick={() => void start()}
        >
          {busy ? "보내는 중…" : "점검 시작"}
        </button>
      </>
    );
  } else {
    action = to ? (
      <button className={cx(s.btn, s.pri, s.full)} type="button" onClick={() => goTo(to)}>
        점검 보기
        <Icon name="arrow" />
      </button>
    ) : null;
  }

  return (
    <>
      <UrlHead
        title="새 점검"
        crumb="새 점검"
        acts={
          <>
            <a className={s.btn} href="#dashboard">
              취소
            </a>
            <InspOpenButton>요약</InspOpenButton>
          </>
        }
      />
      <div className={s.vb}>
        <form
          className={s.vm}
          id="luForm"
          noValidate
          onSubmit={(e) => {
            e.preventDefault();
            void send();
          }}
        >
          {handle ? (
            <div className={s.note} role="note">
              <Icon name="info" />
              <span className={s["lv-grow"]}>
                <b className={s.mono}>{urlHost(handle.input.target_url)}</b>{" "}
                {job ? <UrlPill job={job} verified={verified} /> : null}
                {started ? " 이 브라우저가 이어 보는 점검입니다." : null}
              </span>
              {!started || ended ? (
                <button className={cx(s.btn, s.sm)} type="button" disabled={busy} onClick={disconnectUrl}>
                  새로 입력
                </button>
              ) : null}
            </div>
          ) : null}

          <div className={s.fsec}>
            <div className={s["fsec-h"]}>
              <h2>대상</h2>
            </div>
            <div className={s["fsec-b"]}>
              <div className={s.field}>
                <label className={s.lbl} htmlFor="luUrl">
                  점검할 사이트 주소
                </label>
                <input
                  className={cx(s.inp, s.mono)}
                  id="luUrl"
                  type="url"
                  value={tv}
                  disabled={g.inputLocked}
                  autoComplete="off"
                  spellCheck={false}
                  maxLength={2048}
                  placeholder="https://"
                  aria-describedby="luHost"
                  aria-invalid={!shown && target.trim() && !input && profile ? "true" : "false"}
                  onChange={(e) => setTarget(e.target.value)}
                />
                <div className={s.hostline} id="luHost" aria-live="polite">
                  {hint ? <span className={s.err}>{hint}</span> : null}
                </div>
              </div>
              <TargetApprovalPanel
                requestLimit={profile?.workflow_request_limit}
                allowStateChanges={profile?.workflow?.allow_reversible_writes}
                url={tv}
                disabled={busy || started}
                onChanged={() => {
                  if (handle && !job?.preparation_started) void runUrlAction("validate");
                }}
              />
            </div>
          </div>

          <div className={s.fsec}>
            <div className={s["fsec-h"]}>
              <h2>분석 모델</h2>
            </div>
            <div className={s["fsec-b"]}>
              {externals.length ? (
                <div className={s.seg} role="radiogroup" aria-label="모델 실행 위치">
                  {(
                    [
                      ["local", "로컬 실행", locals.length],
                      ["external", "외부 API", externals.length],
                    ] as const
                  ).map(([k, label, n]) => (
                    <label key={k}>
                      <input
                        type="radio"
                        name="nModel"
                        value={k}
                        checked={shownKind === k}
                        disabled={g.inputLocked || !n}
                        onChange={() => {
                          setKind(k);
                          setCredentialReady(false);
                        }}
                      />
                      <span>{label}</span>
                    </label>
                  ))}
                </div>
              ) : null}
              <div className={s.field}>
                <label className={s.lbl} htmlFor="luProfile">
                  모델
                </label>
                <select
                  className={s.inp}
                  id="luProfile"
                  value={pv}
                  disabled={g.inputLocked || !choices.length}
                  onChange={(e) => setProfileId(e.target.value)}
                >
                  {(shown ? profiles.filter((p) => p.id === pv) : choices).map((p) => (
                    <option key={p.id} value={p.id}>
                      {p.model_label}
                    </option>
                  ))}
                  {!profiles.length ? <option value="">설치된 모델 없음</option> : null}
                </select>
              </div>
              {external && !shown ? (
                <ExternalModelSettings
                  key={`${pv}:${resetSeq}`}
                  profile={profile ?? undefined}
                  disabled={g.inputLocked}
                  onReady={setCredentialReady}
                />
              ) : null}
            </div>
          </div>

          <div className={s.fsec}>
            <div className={s["fsec-h"]}>
              <h2>작업</h2>
            </div>
            <div className={s["fsec-b"]}>
              <div className={s.field}>
                <label className={s.lbl} htmlFor="luName">
                  작업 이름{profile?.workflow ? <span className={s.req}>필수</span> : null}
                </label>
                <input
                  className={s.inp}
                  id="luName"
                  value={nv}
                  maxLength={160}
                  disabled={g.inputLocked}
                  autoComplete="off"
                  placeholder={tv ? `${urlHost(tv)} 점검` : ""}
                  onChange={(e) => setName(e.target.value)}
                />
              </div>
              {profile?.workflow ? (
                <>
                  <div className={s.field}>
                    <span className={s.lbl}>진단 모드</span>
                    <div className={s.opts} role="radiogroup" aria-label="진단 모드">
                      {MODES.map(([id, label, help]) => (
                        <label className={s.opt} key={id}>
                          <input
                            type="radio"
                            name="nSend"
                            value={id}
                            checked={mv === id}
                            disabled={g.inputLocked}
                            onChange={() => setWorkflowMode(id)}
                          />
                          <b>{label}</b>
                          <small>{help}</small>
                        </label>
                      ))}
                    </div>
                  </div>
                  <div className={s.field}>
                    <label className={s.lbl} htmlFor="nExcl">
                      제외 경로 · 선택
                    </label>
                    <textarea
                      id="nExcl"
                      className={s.inp}
                      value={ev}
                      disabled={g.inputLocked}
                      maxLength={2048}
                      onChange={(e) => setExcluded(e.target.value)}
                      placeholder={"/api/admin/reset\n/api/users/*/delete"}
                    />
                  </div>
                </>
              ) : null}
            </div>
          </div>

          <div className={s.formbar}>
            {handle ? (
              <InspOpenButton>시작 단계 보기</InspOpenButton>
            ) : (
              <>
                <button className={cx(s.btn, s.pri)} type="submit" disabled={!canSend}>
                  승인 범위 확인
                </button>
                <span className={s.why}>{why.join(" · ")}</span>
              </>
            )}
          </div>
        </form>

        <aside className={s.vi} aria-label="점검 요약">
          <div className={s["vi-head"]}>
            <div>
              <div className={s.eyebrow}>시작 전 확인</div>
              <h2>점검 요약</h2>
            </div>
            <CloseInspButton label="요약" />
          </div>
          <div className={s.isec} style={{ borderTop: 0, marginTop: 10, paddingTop: 0 }}>
            <dl className={s.sumkv}>
              <dt>대상</dt>
              <dd className={s.mono}>{shown?.target_url ?? input?.target_url ?? (tv.trim() || "—")}</dd>
              <dt>모델</dt>
              <dd>{profile ? `${external ? "외부" : "로컬"} · ${profile.model_label}` : "—"}</dd>
              {profile?.workflow ? (
                <>
                  <dt>진단 모드</dt>
                  <dd>{mv === "ai_auto_v1" ? "AI 자동" : "사용자 승인"}</dd>
                  <dt>제외 경로</dt>
                  <dd>{excludedCount ? `${excludedCount}개` : "없음"}</dd>
                </>
              ) : null}
              <dt>이름</dt>
              <dd>{nv.trim() || "—"}</dd>
            </dl>
          </div>
          <div className={s.isec}>
            <h3>이 점검에 적용되는 안전 정책</h3>
            {scope ? (
              <ul className={s.policy} id="luPolicy">
                <Rule
                  t="승인된 대상과 경로만 요청"
                  sub={`${scope.origin} · ${scope.path_prefixes.join(", ")} · ${scope.methods.join("/")}`}
                />
                <Rule
                  t={
                    job?.input.workflow_mode === "ai_auto_v1"
                      ? "데이터를 바꾸는 요청은 자동 승인 안 함"
                      : "데이터를 바꾸는 요청은 승인한 것만 보냄"
                  }
                />
                {excludedCount ? <Rule t="제외 경로는 요청 안 함" sub={`${excludedCount}개`} /> : null}
                <Rule
                  t="요청 · 시간 상한"
                  sub={`대상 요청 ${scope.budget.requests}회 · ${scope.budget.duration_seconds}초 · 모델 요청 ${scope.budget.model_requests}회`}
                />
                {scope.model ? (
                  <Rule
                    t="외부 모델로 대상 응답 전송"
                    sub={`${scope.model.provider} · 비용 상한 ${usdText(scope.budget.cost_usd_micros)}`}
                  />
                ) : null}
                <Rule t="모든 요청과 승인 결정 기록" sub={`승인 만료 ${scope.expires_at}`} />
              </ul>
            ) : (
              <p className={s.help}>승인 범위를 확인하면 서버가 정한 범위 · 상한이 여기에 나옵니다.</p>
            )}
          </div>
          <div className={s.isec}>
            {action}
            <UrlNotices />
          </div>
        </aside>
        <PaneToggle />
      </div>
    </>
  );
}
