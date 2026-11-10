"use client";

// URL 진단(v2) 시작 전 두 화면: 새 점검(URL · 설치 프로필 · 이름 → 초안 + 범위 확인)과
// 범위 확인(서버가 돌려준 실제 범위 · 모델 · 예산을 보고 확인 상자 + 시작). 시작 전에는 task · Planner ·
// 대상 송신 · 단회 권위를 만들지 않는다(서버 계약). URL 은 요청일 뿐이고 허가는 서버 설치 범위와 E② 가 정한다.

import { type ReactNode, useEffect, useRef, useState } from "react";

import { type URLDiagnosisInput, urlDiagnosisInput, type WorkflowMode } from "@/lib/diagnosis/diagnosis-url";

import { cx } from "../../lib/cx";
import { LoadingRow } from "../../live/parts";
import { runUrlAction, setUrlConfirmed, useUrl, useUrlGates } from "../../live/urlDiagnosis";
import { urlHost, urlTargetHint, urlViewFor } from "../../live/urlMap";
import { useShell } from "../../shell";
import { useWb } from "../../store";
import { Icon } from "../../ui/icons";
import { CloseInspButton, InspHead, InspOpenButton, Isec } from "../../ui/parts";
import s from "../../workbench.module.css";
import { ExternalModelSettings } from "./ExternalModelSettings";
import { TargetApprovalPanel } from "./TargetApprovalPanel";
import {
  reasonText,
  UrlDisconnect,
  UrlFacts,
  UrlHead,
  UrlNoJob,
  UrlNotices,
  UrlPhaseBar,
  UrlPill,
  UrlRefreshButton,
  UrlScopeList,
  useUrlHost,
} from "./urlParts";

/** 초안 요청 본문의 version (서버 계약 diagnosis-api.v2). 미리보기 글에만 쓴다. */
const API_VERSION = "diagnosis-api.v2";

/** 갈 화면 → 버튼 글. */
const GO_LABEL = { scope: "범위 확인으로", live: "점검 진행 보기", report: "보고서 보기" } as const;

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

/** live 새 점검 (URL 진단). */
export function UrlNewScanLive() {
  const { goTo } = useShell();
  const cap = useUrl((st) => st.cap);
  const handle = useUrl((st) => st.handle);
  const job = useUrl((st) => st.job);
  const verified = useUrl((st) => st.verified);
  const busy = useUrl((st) => st.busy);
  const resetSeq = useUrl((st) => st.resetSeq);
  const g = useUrlGates();
  const prefill = useWb((st) => st.prefill);
  const profiles = cap.kind === "v2" ? cap.catalog.profiles : [];
  const first = profiles[0]?.id ?? "";

  const [target, setTarget] = useState("");
  const [profileId, setProfileId] = useState(first);
  const [name, setName] = useState("");
  const [workflowMode, setWorkflowMode] = useState<WorkflowMode>("user_approval_v1");
  const [excluded, setExcluded] = useState("");
  const [externalOpen, setExternalOpen] = useState(false);
  const [credentialReady, setCredentialReady] = useState(false);
  const externalProfile = profiles.find((p) => p.model?.mode === "openrouter");
  const seenSeq = useRef(useWb.getState().prefill?.seq ?? 0);
  const seenReset = useRef(resetSeq);

  // '다른 진단 만들기' 뒤에는 폼을 비운다 (같은 입력으로 겹쳐 만들지 않게)
  useEffect(() => {
    if (resetSeq === seenReset.current) return;
    seenReset.current = resetSeq;
    setTarget("");
    setName("");
    setProfileId(externalOpen && externalProfile ? externalProfile.id : first);
  }, [resetSeq, first, externalOpen, externalProfile]);

  // 다른 화면의 '이 주소로 다시 점검' (조회 중인 진단이 없을 때만)
  useEffect(() => {
    if (!prefill || prefill.seq === seenSeq.current) return;
    seenSeq.current = prefill.seq;
    if (!useUrl.getState().handle) setTarget(prefill.url);
  }, [prefill]);

  // 조회 중인 진단이 있으면 그 입력을 보인다 (잠금)
  const shown = handle?.input;
  const tv = shown ? shown.target_url : target;
  const pv = shown ? shown.profile_id : profileId;
  const nv = shown ? (shown.display_name ?? "") : name;
  const profile = profiles.find((p) => p.id === pv) ?? null;
  const input = shown
    ? null
    : draftInput(
        target,
        profileId,
        name,
        profile?.workflow ? workflowMode : undefined,
        profile?.workflow ? excluded : "",
      );

  const hint = urlTargetHint(tv);
  const why: string[] = [];
  if (!shown) {
    if (!target.trim()) why.push("주소를 입력하세요");
    else if (hint) why.push(hint);
    else if (!input && profile) why.push("주소 형식을 확인하세요 (정본 HTTPS 주소)");
    if (!profiles.length) why.push("설치된 모델 프로필이 없습니다 (설치자 설정 필요)");
    else if (!profile) why.push("모델 프로필을 고르세요");
  }
  const canSend =
    !shown &&
    !!input &&
    g.canValidate &&
    (!profile?.workflow || !!name.trim()) &&
    !busy &&
    (!profile?.model?.external_transfer || (externalOpen && credentialReady));

  const send = async () => {
    if (!canSend || !input) return;
    if (await runUrlAction("validate", input)) goTo("scope");
  };

  const preview = { version: API_VERSION, input: shown ?? input ?? { target_url: "", profile_id: pv || "" } };
  const sendLabel = busy && !job ? "확인 중…" : "승인 범위 확인";

  let current: ReactNode = null;
  if (handle) {
    const host = urlHost(handle.input.target_url);
    const to = job ? urlViewFor(job) : null;
    current = (
      <div className={s.fsec}>
        <div className={s["fsec-h"]}>
          <h2>조회 중인 진단</h2>
          <p>이 브라우저에 이어 볼 진단이 있어 새 입력을 잠갔습니다.</p>
        </div>
        <div className={s["fsec-b"]}>
          <div className={s.hostline}>
            <span className={s.host}>{host}</span>
            {job ? <UrlPill job={job} verified={verified} /> : <span className={cx(s.pill, s.wait)}>상태 미확인</span>}
          </div>
          {job?.reason_code ? <p className={s.help}>{`서버 사유: ${reasonText(job.reason_code)}`}</p> : null}
          <div className={s["vh-acts"]}>
            {to ? (
              <button className={cx(s.btn, s.pri, s.sm)} type="button" onClick={() => goTo(to)}>
                {GO_LABEL[to]}
                <Icon name="arrow" />
              </button>
            ) : null}
            <UrlRefreshButton />
          </div>
          <UrlNotices />
        </div>
        <UrlDisconnect />
      </div>
    );
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
          {current}
          <div className={s.fsec}>
            <div className={s["fsec-h"]}>
              <h2>대상</h2>
              <p>
                URL 은 진단 요청입니다. 실제 대상 · 경로 · 메서드 · 모델 · 예산은 서버에 설치된 승인 범위가 정합니다.
              </p>
            </div>
            <div className={s["fsec-b"]}>
              <div className={s.field}>
                <label className={s.lbl} htmlFor="luUrl">
                  진단 대상 URL<span className={s.req}>필수</span>
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
                  placeholder="https://승인된-대상/"
                  aria-describedby="luHost"
                  aria-invalid={!shown && target.trim() && !input ? "true" : "false"}
                  onChange={(e) => setTarget(e.target.value)}
                />
                <div className={s.hostline} id="luHost" aria-live="polite">
                  {hint ? (
                    <span className={s.err}>{hint}</span>
                  ) : (
                    <span className={s.help}>
                      HTTPS 주소만 넣으세요. 계정 · 비밀번호, query, fragment 는 넣지 마세요.
                    </span>
                  )}
                </div>
              </div>
            </div>
          </div>

          <div className={s.fsec}>
            <div className={s["fsec-h"]}>
              <h2>로그인</h2>
              <p>대상 계정 연결은 현재 제품에서 지원하지 않습니다.</p>
            </div>
            <fieldset className={s["fsec-b"]} disabled aria-label="대상 로그인 · 미지원">
              <div className={s.seg} role="radiogroup" aria-label="점검 영역">
                <label>
                  <input type="radio" name="nAuth" value="in" />
                  <span>로그인 영역 포함 · 미지원</span>
                </label>
                <label>
                  <input type="radio" name="nAuth" value="pub" defaultChecked />
                  <span>공개 영역만</span>
                </label>
              </div>
              <div className={s.seg} role="radiogroup" aria-label="대상 로그인 방법">
                <label>
                  <input type="radio" name="nHow" value="form" />
                  <span>아이디 · 비밀번호</span>
                </label>
                <label>
                  <input type="radio" name="nHow" value="sess" />
                  <span>세션 직접 입력</span>
                </label>
              </div>
              {["A", "B"].map((account) => (
                <div className={s.acct} key={account}>
                  <span className={s.an}>계정 {account}</span>
                  <input className={s.inp} placeholder="연결 미지원" aria-label={`계정 ${account} 아이디 · 미지원`} />
                  <input
                    className={s.inp}
                    type="password"
                    placeholder="입력하지 마세요"
                    autoComplete="off"
                    aria-label={`계정 ${account} 비밀번호 · 미지원`}
                  />
                </div>
              ))}
              <p className={s.help}>
                비밀번호·쿠키를 입력하거나 저장하지 않습니다. 인증된 계정 간 접근 검사는 아직 연결되지 않았습니다.
              </p>
            </fieldset>
          </div>
          <div className={s.fsec}>
            <div className={s["fsec-h"]}>
              <h2>분석 모델</h2>
              <p>설치자가 승인한 모델 프로필만 고를 수 있습니다. 다른 모델로 바꿔 돌리지 않습니다.</p>
            </div>
            <div className={s["fsec-b"]}>
              <button
                type="button"
                className={s.btn}
                aria-expanded={externalOpen}
                disabled={g.inputLocked}
                onClick={() => {
                  const next = !externalOpen;
                  setExternalOpen(next);
                  setCredentialReady(false);
                  setProfileId(
                    next ? (externalProfile?.id ?? "") : (profiles.find((p) => !p.model?.external_transfer)?.id ?? ""),
                  );
                }}
              >
                외부 LLM 사용하기
              </button>
              {externalOpen ? (
                <ExternalModelSettings
                  key={`${externalProfile?.id ?? "unavailable"}:${resetSeq}`}
                  profile={externalProfile}
                  disabled={g.inputLocked}
                  onReady={setCredentialReady}
                />
              ) : null}
              <div className={s.field}>
                <label className={s.lbl} htmlFor="luProfile">
                  승인된 모델 프로필
                </label>
                <select
                  className={s.inp}
                  id="luProfile"
                  value={pv}
                  disabled={g.inputLocked}
                  onChange={(e) => setProfileId(e.target.value)}
                >
                  <option value="">설치된 프로필 선택</option>
                  {profiles.map((p) => (
                    <option key={p.id} value={p.id}>
                      {`${p.label || p.id} · ${p.model_label || p.model_profile_ref}`}
                    </option>
                  ))}
                </select>
                {profiles.length ? null : (
                  <span className={s.help}>제공된 모델 프로필이 없습니다. 설치자 설정이 필요합니다.</span>
                )}
              </div>
            </div>
          </div>

          <TargetApprovalPanel
            requestLimit={profile?.workflow_request_limit}
            allowStateChanges={profile?.workflow?.allow_reversible_writes}
            url={tv}
            disabled={busy}
            onChanged={() => {
              if (handle && !job?.preparation_started) void runUrlAction("validate");
            }}
          />
          <div className={s.fsec}>
            <div className={s["fsec-h"]}>
              <h2>작업</h2>
              <p>이름은 화면 표시용입니다.</p>
            </div>
            <div className={s["fsec-b"]}>
              <div className={s.field}>
                <label className={s.lbl} htmlFor="luName">
                  진단 이름 {profile?.workflow ? "· 필수" : "· 선택"}
                </label>
                <input
                  className={s.inp}
                  id="luName"
                  value={nv}
                  maxLength={160}
                  disabled={g.inputLocked}
                  autoComplete="off"
                  onChange={(e) => setName(e.target.value)}
                />
              </div>
              <div className={s.field}>
                <span className={s.lbl} id="nGoalL">
                  점검 목표 · 현재 설치 정책 사용
                </span>
                <fieldset className={s.goals} aria-labelledby="nGoalL">
                  {["주문·결제 권한 경계", "관리자 기능 접근", "비로그인 노출"].map((goal) => (
                    <label className={s.goal} key={goal}>
                      <input type="checkbox" disabled />
                      <span>{goal} · 선택 미지원</span>
                    </label>
                  ))}
                </fieldset>
              </div>
              <div className={s.two}>
                <div className={s.field}>
                  <span className={s.lbl}>점검 범위</span>
                  <div className={s.seg} role="radiogroup" aria-label="점검 범위">
                    {["설치 상한", "심화", "제한 없음"].map((depth, i) => (
                      <label key={depth}>
                        <input type="radio" name="nDepth" disabled defaultChecked={i === 0} />
                        <span>{depth}</span>
                      </label>
                    ))}
                  </div>
                </div>
                <div className={s.field}>
                  <label className={s.lbl} htmlFor="nWorkers">
                    워커 수
                  </label>
                  <select className={s.inp} id="nWorkers" disabled>
                    <option>설치 정책 사용</option>
                  </select>
                </div>
              </div>
              <p className={s.help}>
                범위·워커 수를 화면에서 확대하지 않습니다. 탐색 상한은 승인 범위 확인에 표시합니다.
              </p>
              {profile?.workflow ? (
                <>
                  <div className={s.field}>
                    <span className={s.lbl}>진단 모드 · HTTP 송신 권한과 별개</span>
                    <div className={s.opts} role="radiogroup" aria-label="진단 모드">
                      {(
                        [
                          [
                            "ai_auto_v1",
                            "AI 자동 모드",
                            "사전 승인한 읽기 범위에서 탐색·심화 진단을 이어갑니다. 상태 변경을 자동 승인하지 않습니다.",
                          ],
                          [
                            "user_approval_v1",
                            "사용자 승인 모드",
                            "초기 탐색 후 엔드포인트를 선택하고 상태 변경 가능 요청은 전송 전에 개별 확인합니다.",
                          ],
                        ] as const
                      ).map(([id, label, help]) => (
                        <label className={s.opt} key={id}>
                          <input
                            type="radio"
                            name="nSend"
                            value={id}
                            checked={(shown?.workflow_mode ?? workflowMode) === id}
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
                      제외 경로 · 한 줄에 하나
                    </label>
                    <textarea
                      id="nExcl"
                      className={s.inp}
                      value={shown?.excluded_paths ?? excluded}
                      disabled={g.inputLocked}
                      maxLength={2048}
                      onChange={(e) => setExcluded(e.target.value)}
                      placeholder={"/api/admin/reset\n/api/users/*/delete"}
                    />
                    <p className={s.help}>
                      경로 접두어와 전체 구간 *를 지원합니다. 제외 범위는 서버에 저장하며 새 발견으로 넓히지 않습니다.
                    </p>
                  </div>
                </>
              ) : (
                <div className={s.note} role="note">
                  <Icon name="info" />
                  <span>
                    이 설치에는 탐색·선택 워크플로가 활성화되지 않았습니다. 기존 승인 범위 진단만 실행할 수 있습니다.
                  </span>
                </div>
              )}
            </div>
          </div>

          <div className={s.formbar}>
            <button className={cx(s.btn, s.pri)} type="submit" disabled={!canSend}>
              {sendLabel}
            </button>
            <span className={s.why}>{why.join(" · ")}</span>
            {handle ? null : <UrlNotices />}
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
              <dd className={s.mono}>{shown?.target_url ?? input?.target_url ?? "—"}</dd>
              <dt>모델</dt>
              <dd>{profile ? `${profile.label} · ${profile.model_label}` : "—"}</dd>
              {profile?.model ? (
                <>
                  <dt>제공자</dt>
                  <dd>{profile.model.provider} · OpenRouter</dd>
                  <dt>외부 전송</dt>
                  <dd>대상 응답·도구 출력 전송과 USD 상한을 범위 확인 화면에서 동의해야 합니다.</dd>
                </>
              ) : null}
              <dt>이름</dt>
              <dd>{nv.trim() || "—"}</dd>
            </dl>
          </div>
          <div className={s.isec}>
            <h3>서버에 보낼 내용 · POST /api/diagnoses/v2/drafts</h3>
            <pre className={s.code} id="luBody">
              {JSON.stringify(preview, null, 2)}
            </pre>
            <p className={s.help}>
              영수증 키(Idempotency-Key)는 브라우저가 새로 만듭니다. 비밀번호 · 세션 같은 비밀 값은 보내지 않습니다.
            </p>
          </div>
          <div className={s.isec}>
            <div className={s.note} role="note">
              <Icon name="info" />
              <div>
                초안 · 범위 확인은 DNS · HTTP · 모델을 부르지 않습니다. 시작은 범위 확인 화면에서 확인 상자와 시작
                버튼으로 따로 합니다.
              </div>
            </div>
            <button className={cx(s.btn, s.pri, s.full)} type="button" disabled={!canSend} onClick={() => void send()}>
              {sendLabel}
            </button>
            <span className={s.why}>{why.join(" · ")}</span>
            {handle ? null : <UrlNotices />}
          </div>
        </aside>
      </div>
    </>
  );
}

/** live 범위 확인 (URL 진단): 서버가 확인한 범위 · 예산 → 확인 상자 → 진단 시작. */
export function UrlScopeLive() {
  const { goTo } = useShell();
  const handle = useUrl((st) => st.handle);
  const job = useUrl((st) => st.job);
  const verified = useUrl((st) => st.verified);
  const busy = useUrl((st) => st.busy);
  const confirmed = useUrl((st) => st.confirmed);
  const g = useUrlGates();
  const host = useUrlHost();

  const head = (acts?: ReactNode) => (
    <UrlHead
      title="범위 확인"
      crumb="범위 확인"
      host={host}
      pill={job ? <UrlPill job={job} verified={verified} /> : null}
      acts={acts}
    />
  );

  if (!handle) {
    return (
      <>
        {head()}
        <div className={s.vm}>
          <UrlNoJob what="범위 확인" />
        </div>
      </>
    );
  }
  if (!job) {
    return (
      <>
        {head(<UrlRefreshButton />)}
        <div className={s.vm}>
          {busy ? <LoadingRow text="진단 상태를 확인하는 중…" /> : null}
          <UrlNotices />
        </div>
      </>
    );
  }

  const start = async () => {
    if (await runUrlAction("start")) goTo(job.input.workflow_mode ? "crawl" : "live");
  };
  const revalidate = () => void runUrlAction("validate");

  let action: ReactNode;
  if (job.state === "ready") {
    action = (
      <>
        {job.scope?.approval_mode === "confirm_every_request" ? (
          <div className={cx(s.note, s.warn)} role="note">
            <Icon name="alert" />
            <span>
              요청별 확인이 필요한 프로필은 현재 작업대에서 시작할 수 없습니다. 운영자에게 요청별 승인 경로를 확인해
              주세요.
            </span>
          </div>
        ) : null}
        <label className={s.ckline}>
          <input
            type="checkbox"
            checked={confirmed}
            disabled={!g.canStart}
            onChange={(e) => setUrlConfirmed(e.target.checked)}
          />
          {job.scope?.model?.external_transfer
            ? "대상 응답·도구 출력의 외부 전송과 표시된 USD 비용 상한에 동의하고, 서버가 확인한 범위 · 모델 · 예산으로 단회 진단을 시작합니다."
            : "서버가 확인한 범위 · 모델 · 예산으로 준비와 단회 진단을 시작합니다."}
        </label>
        <div className={s.hostline}>
          <button
            className={cx(s.btn, s.pri)}
            type="button"
            disabled={!g.canStart || !confirmed}
            onClick={() => void start()}
          >
            {busy ? "보내는 중…" : "진단 시작"}
          </button>
          <span className={s.why}>{g.canStart && !confirmed ? "확인 상자를 켜야 시작합니다" : ""}</span>
        </div>
      </>
    );
  } else if (job.state === "awaiting_approval") {
    action = (
      <>
        <div className={cx(s.note, s.warn)} role="note">
          <Icon name="alert" />
          <span>
            {job.reason_code
              ? `아직 시작할 수 없습니다 · ${reasonText(job.reason_code)}`
              : "설치자 승인이 필요합니다. URL 입력이나 사용자 확인만으로 실행 권위가 생기지 않습니다."}
          </span>
        </div>
        <div>
          <button className={cx(s.btn, s.sm)} type="button" disabled={!g.canValidate} onClick={revalidate}>
            승인 범위 다시 확인
          </button>
        </div>
      </>
    );
  } else {
    action = (
      <div className={s.note} role="note">
        <Icon name="info" />
        <span className={s["lv-grow"]}>이미 시작한 진단입니다. 범위는 바꿀 수 없습니다.</span>
        <button className={cx(s.btn, s.sm)} type="button" onClick={() => goTo(urlViewFor(job))}>
          {GO_LABEL[urlViewFor(job)]}
        </button>
      </div>
    );
  }

  return (
    <>
      {head(
        <>
          <UrlRefreshButton />
          <InspOpenButton>진단 정보</InspOpenButton>
        </>,
      )}
      <div className={s.vb}>
        <div className={s.vm}>
          <UrlPhaseBar job={job} />
          <p className={s.lead}>
            서버가 현재 진단 URL의 승인 기록과 대조해 돌려준 값입니다. 이 범위 밖 주소 · 메서드 · 다른 subdomain · 포트
            · redirect 목적지는 자동으로 허가하지 않습니다.
          </p>
          <section className={s.sec} aria-labelledby="luScopeH">
            <div className={s.sh}>
              <h2 id="luScopeH">서버가 확인한 승인 범위</h2>
            </div>
            {job.scope ? (
              <UrlScopeList scope={job.scope} />
            ) : (
              <p className={s.empty}>서버가 아직 승인 범위를 돌려주지 않았습니다.</p>
            )}
          </section>
          <section className={s.sec} aria-labelledby="luStartH">
            <div className={s.sh}>
              <h2 id="luStartH">시작</h2>
            </div>
            {action}
            <UrlNotices />
          </section>
        </div>
        <aside className={s.vi} aria-label="진단 정보">
          <InspHead eyebrow={host ?? ""} title="진단 정보" />
          <Isec>
            <UrlFacts job={job} />
          </Isec>
          <UrlDisconnect />
        </aside>
      </div>
    </>
  );
}
