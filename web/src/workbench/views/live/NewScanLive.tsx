"use client";

// 실제 엔진 새 점검: 시안(views/NewScan.tsx)과 같은 폼 모양이지만 엔진이 받는 칸만 살린다.
// 대상 URL · 이름 · 목표 · LLM 프로필 · 제외 경로(→ 작업 규칙 block·fuzzy_url) · 커버리지 → POST /api/tasks.
// 소유 확인 · 로그인 계정 · 전송 범위 · 깊이 · 워커 수는 엔진에 없어 한 묶음으로 막아 둔다(값을 보내지 않음).
// 엔진은 만들자마자 점검을 시작한다 → 만든 점검을 고르고 점검 진행 화면으로 간다.
// 이 폼은 URL 진단(v2)이 없는 서버에서만 보인다(views/live/byMode). 통제된 URL 진단과 섞이지 않게 맨 위에 밝힌다.

import { type ReactNode, useEffect, useRef, useState } from "react";

import { cx } from "../../lib/cx";
import { DEPTHS, WORKERS } from "../../lib/newScan";
import { createTask, fetchLlmProfiles, type LlmProfile } from "../../live/api";
import { recentTargets } from "../../live/dashboard";
import { reloadTasks, useLiveTasks } from "../../live/feeds";
import { isLocalBaseUrl, isUnsupported, relTime } from "../../live/map";
import {
  initialLiveForm,
  type LiveScanForm,
  liveScanView,
  normalizeLiveUrl,
  requestPreview,
  syncLiveName,
} from "../../live/newScan";
import { ErrorNote, LoadingRow, ModeNote, Unsupported, useNow } from "../../live/parts";
import { useSource } from "../../live/source";
import { usePoll } from "../../live/usePoll";
import { useShell } from "../../shell";
import { useWb } from "../../store";
import { Icon } from "../../ui/icons";
import { CloseInspButton, InspOpenButton, MenuButton } from "../../ui/parts";
import s from "../../workbench.module.css";

/** 엔진 미지원 칸의 막힌 `.seg` 묶음 (첫 값이 골라진 모양만). */
function DisabledSeg({ label, options }: { label: string; options: readonly string[] }) {
  return (
    <div className={s.seg} role="radiogroup" aria-label={label} aria-disabled="true" style={{ opacity: 0.6 }}>
      {options.map((o, i) => (
        <label key={o}>
          <input type="radio" name={`lv-${label}`} value={o} defaultChecked={i === 0} disabled />
          <span>{o}</span>
        </label>
      ))}
    </div>
  );
}

/** 엔진 미지원 칸 하나 (이름 + 딱지 + 막힌 입력). */
function UnsupportedField({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className={s.field}>
      <span className={s.lbl}>
        {label} <Unsupported chip />
      </span>
      {children}
    </div>
  );
}

/** 프로필 고르기 칸 아래 한 줄: 로컬/외부 추정. */
function ProfileLine({ profile }: { profile: LlmProfile | null }) {
  if (!profile) {
    return (
      <div className={s.hostline}>
        <span className={s.help}>프로필을 지정하지 않으면 엔진의 활성 LLM 설정으로 점검합니다.</span>
      </div>
    );
  }
  const local = isLocalBaseUrl(profile.base_url);
  return (
    <>
      <div className={s.hostline}>
        <span className={cx(s.pill, local ? s.ok : s.wait)}>{local ? "로컬(추정)" : "외부(추정)"}</span>
        <span className={s.help}>
          {`${profile.format || "형식 모름"} · ${profile.model || "모델 이름 없음"} · 서버 주소로 짐작한 값입니다`}
        </span>
      </div>
      {local ? null : (
        <div className={cx(s.note, s.warn)}>
          <Icon name="alert" />
          <div>대상 데이터와 프롬프트가 외부 제공자로 전송될 수 있습니다. 민감한 환경에서는 로컬 모델을 쓰세요.</div>
        </div>
      )}
    </>
  );
}

export function NewScanLive() {
  const { route, goTo } = useShell();
  const visible = route === "new";
  const now = useNow();
  const tasks = useLiveTasks();
  const selectTask = useSource((st) => st.selectTask);
  const prefill = useWb((st) => st.prefill);
  const profiles = usePoll(() => fetchLlmProfiles(), [], { enabled: visible });

  const [f, setF] = useState<LiveScanForm>(initialLiveForm);
  const [sending, setSending] = useState(false);
  const [createErr, setCreateErr] = useState<unknown>(null);
  const lock = useRef(false);
  const urlRef = useRef<HTMLInputElement>(null);
  // 마운트 전에 쌓인 주소 채우기 요청은 쓰지 않는다 (다른 화면의 '이 주소로 다시 점검' 만)
  const seenSeq = useRef(useWb.getState().prefill?.seq ?? 0);

  useEffect(() => {
    if (!prefill || prefill.seq === seenSeq.current) return;
    seenSeq.current = prefill.seq;
    setF((p) => syncLiveName({ ...p, url: prefill.url, nameTouched: false }));
  }, [prefill]);

  const list = profiles.unsupported ? null : profiles.data;
  const v = liveScanView(f, list);
  const picked = list?.find((p) => p.id === v.profileId) ?? null;
  const why = v.why.join(" · ");
  const blocked = !v.input || sending;
  const recent = recentTargets(tasks.data?.tasks ?? [], 5);

  const set = (patch: Partial<LiveScanForm>) => setF((p) => syncLiveName({ ...p, ...patch }));

  const start = async () => {
    const input = v.input;
    if (lock.current || !input) return;
    lock.current = true;
    setSending(true);
    setCreateErr(null);
    try {
      const created = await createTask(input);
      if (created.id) selectTask(created.id);
      reloadTasks();
      goTo("live");
    } catch (e) {
      setCreateErr(e);
    } finally {
      lock.current = false;
      setSending(false);
    }
  };

  let errBox: ReactNode = null;
  if (createErr) {
    errBox = isUnsupported(createErr) ? <ModeNote what="새 점검 만들기" /> : <ErrorNote error={createErr} />;
  }
  const startLabel = sending ? "만드는 중…" : "점검 시작";

  // 프로필 칸
  let profileBody: ReactNode;
  if (profiles.unsupported) {
    profileBody = (
      <>
        <ModeNote what="LLM 프로필" />
        <ProfileLine profile={null} />
      </>
    );
  } else if (!profiles.data && profiles.error) {
    profileBody = <ErrorNote error={profiles.error} onRetry={profiles.reload} />;
  } else if (!profiles.data) {
    profileBody = <LoadingRow />;
  } else {
    profileBody = (
      <>
        <div className={s.field}>
          <label className={s.lbl} htmlFor="lvModel">
            LLM 프로필
          </label>
          <select
            className={s.inp}
            id="lvModel"
            value={v.profileId}
            onChange={(e) => setF((p) => ({ ...p, profile: e.target.value }))}
          >
            {profiles.data.map((p) => (
              <option key={p.id} value={p.id}>
                {`${p.name || `프로필 #${p.id}`} · ${p.model || "모델 이름 없음"}${p.is_default ? " · 기본" : ""}`}
              </option>
            ))}
            <option value="">지정 안 함 (엔진 활성 설정)</option>
          </select>
        </div>
        <ProfileLine profile={picked} />
      </>
    );
  }

  const t = v.target;
  const preview = requestPreview(v.draft);

  // 주소 아래 한 줄: 오류 · 내부 주소 · 외부 주소(소유 확인 미지원 안내)
  let hostLine: ReactNode;
  if (!t.ok) hostLine = <span className={s.err}>{t.error}</span>;
  else if (t.internal) {
    hostLine = (
      <>
        <span className={cx(s.pill, s.ok)}>내부 주소</span>
        <span className={s.help}>로컬·사설망 주소입니다</span>
      </>
    );
  } else {
    hostLine = (
      <>
        <span className={cx(s.pill, s.wait)}>외부 주소</span>
        <span className={s.help}>소유 확인은 엔진 미지원 — 점검 권한이 있는 대상인지 직접 확인하세요</span>
      </>
    );
  }

  return (
    <>
      <header className={s.vh}>
        <div className={s["vh-top"]}>
          <MenuButton />
          <nav className={s.crumb} aria-label="위치">
            <a href="#dashboard">작업대</a>
            <span className={s.sep}>/</span>
            <span>새 점검</span>
          </nav>
        </div>
        <div className={s["vh-row"]}>
          <div className={s["vh-title"]}>
            <h1 tabIndex={-1}>새 점검</h1>
          </div>
          <div className={s["vh-acts"]}>
            <a className={s.btn} href="#dashboard">
              취소
            </a>
            <InspOpenButton>요약</InspOpenButton>
          </div>
        </div>
      </header>
      <div className={s.vb}>
        <form
          className={s.vm}
          id="lvForm"
          noValidate
          onSubmit={(e) => {
            e.preventDefault();
            void start();
          }}
        >
          <div className={cx(s.note, s.warn)} role="note" id="lvKind">
            <Icon name="alert" />
            <span>
              이 서버는 URL 진단(통제된 AI DAST, /api/diagnoses/v2)을 제공하지 않아 일반 ARTEX 작업(POST /api/tasks)으로
              만듭니다. 설치 승인 범위 확인 · 단회 실행 권위 · E② 요청 통제가 걸린 진단이 아닙니다.
            </span>
          </div>
          <div className={s.fsec}>
            <div className={s["fsec-h"]}>
              <h2>대상</h2>
              <p>점검 권한이 있는 주소만 넣으세요. 엔진은 설명 안의 URL 을 대상으로 잡습니다.</p>
            </div>
            <div className={s["fsec-b"]}>
              <div className={s.field}>
                <label className={s.lbl} htmlFor="lvUrl">
                  점검할 사이트 주소<span className={s.req}>필수</span>
                </label>
                <input
                  ref={urlRef}
                  className={cx(s.inp, s.mono)}
                  id="lvUrl"
                  type="url"
                  value={f.url}
                  autoComplete="url"
                  spellCheck={false}
                  placeholder="https://shop.example.com"
                  aria-describedby="lvHost"
                  aria-invalid={!t.ok && f.url.trim() ? "true" : "false"}
                  onChange={(e) => set({ url: e.target.value })}
                  onBlur={(e) => {
                    const n = normalizeLiveUrl(e.currentTarget.value);
                    if (n !== null) set({ url: n });
                  }}
                />
                <div className={s.hostline} id="lvHost" aria-live="polite">
                  {hostLine}
                </div>
              </div>
              <div className={s.field}>
                <span className={s.lbl}>최근 점검한 주소</span>
                {recent.length ? (
                  <div className={s.chiprow}>
                    {recent.map((r) => (
                      <button
                        key={r.host}
                        className={s.rchip}
                        type="button"
                        onClick={() => {
                          set({ url: r.url, nameTouched: false });
                          urlRef.current?.focus();
                        }}
                      >
                        {r.host} <small>{now ? relTime(r.createdUnix, now) : ""}</small>
                      </button>
                    ))}
                  </div>
                ) : (
                  <span className={s.help}>
                    {tasks.data ? "아직 점검한 주소가 없습니다." : "점검 목록을 불러오는 중…"}
                  </span>
                )}
              </div>
            </div>
          </div>

          <div className={s.fsec}>
            <div className={s["fsec-h"]}>
              <h2>분석 모델</h2>
              <p>점검을 판단하는 LLM 프로필을 고릅니다.</p>
            </div>
            <div className={s["fsec-b"]}>{profileBody}</div>
          </div>

          <div className={s.fsec}>
            <div className={s["fsec-h"]}>
              <h2>작업</h2>
              <p>이름과 목표, 제외할 경로를 정합니다.</p>
            </div>
            <div className={s["fsec-b"]}>
              <div className={s.field}>
                <label className={s.lbl} htmlFor="lvName">
                  작업 이름<span className={s.req}>필수</span>
                </label>
                <input
                  className={s.inp}
                  id="lvName"
                  value={f.name}
                  aria-describedby="lvNameErr"
                  aria-invalid={v.nameOk ? "false" : "true"}
                  onChange={(e) => {
                    const name = e.target.value;
                    setF((p) => ({ ...p, name, nameTouched: true }));
                  }}
                />
                <span className={s.err} id="lvNameErr" hidden={v.nameOk}>
                  작업 이름을 입력하세요.
                </span>
              </div>
              <div className={s.field}>
                <label className={s.lbl} htmlFor="lvGoal">
                  점검 목표 · 선택
                </label>
                <textarea
                  className={s.inp}
                  id="lvGoal"
                  value={f.goal}
                  placeholder="예: 주문·결제 권한 경계, 관리자 기능 접근"
                  onChange={(e) => setF((p) => ({ ...p, goal: e.target.value }))}
                />
                <span className={s.help}>비워 두면 엔진이 대상과 설명만으로 목표를 나눕니다.</span>
              </div>
              <div className={s.field}>
                <label className={s.lbl} htmlFor="lvExcl">
                  제외 경로 · 선택
                </label>
                <textarea
                  className={s.inp}
                  id="lvExcl"
                  value={f.excl}
                  placeholder={"/api/admin/reset\n/logout"}
                  aria-describedby="lvExclHelp"
                  aria-invalid={v.badExcl.length ? "true" : "false"}
                  onChange={(e) => setF((p) => ({ ...p, excl: e.target.value }))}
                />
                <span className={s.help} id="lvExclHelp">
                  줄마다 하나. 이 글이 URL 에 들어 있는 자산은 엔진이 점검 대상(의도·자산)으로 올리지 않습니다(대소문자
                  무시 부분 일치, 끝의 * 는 지움). 개별 HTTP 요청을 막는 장치는 아닙니다.
                </span>
                {v.badExcl.length ? (
                  <span className={s.err}>{`가운데 * 는 와일드카드가 아닙니다: ${v.badExcl.join(", ")}`}</span>
                ) : null}
              </div>
              <label className={s.ckline}>
                <input type="checkbox" checked={f.coverage} onChange={(e) => set({ coverage: e.target.checked })} />
                커버리지 기록 (점검 범위 안 자산 중 검사한 비율을 엔진이 셉니다)
              </label>
            </div>
          </div>

          <div className={s.fsec}>
            <div className={s["fsec-h"]}>
              <h2>엔진에 없는 설정</h2>
              <p>시안에만 있는 칸입니다. 바꿀 수 없고 엔진에 보내지 않습니다.</p>
            </div>
            <div className={s["fsec-b"]} id="lvUnsupported">
              <div className={s.two}>
                <UnsupportedField label="소유 확인 (DNS TXT)">
                  <button className={cx(s.btn, s.sm)} type="button" disabled>
                    TXT 레코드 확인
                  </button>
                </UnsupportedField>
                <UnsupportedField label="로그인 계정 · 세션">
                  <DisabledSeg label="로그인 계정 · 세션" options={["로그인 영역 포함", "공개 영역만"]} />
                </UnsupportedField>
                <UnsupportedField label="전송 범위 (데이터를 바꾸는 요청)">
                  <DisabledSeg label="전송 범위" options={["승인 후 전송", "전체 허용"]} />
                </UnsupportedField>
                <UnsupportedField label="점검 깊이">
                  <DisabledSeg label="점검 깊이" options={DEPTHS} />
                </UnsupportedField>
                <UnsupportedField label="워커 수">
                  <select className={s.inp} aria-label="워커 수" defaultValue={WORKERS[2]} disabled>
                    {WORKERS.map((w) => (
                      <option key={w}>{w}</option>
                    ))}
                  </select>
                </UnsupportedField>
              </div>
            </div>
          </div>

          <div className={s.formbar}>
            <button className={cx(s.btn, s.pri)} type="submit" disabled={blocked}>
              {startLabel}
            </button>
            <span className={s.why}>{why}</span>
            {errBox}
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
              <dd className={s.mono} id="lvSumTarget">
                {t.ok ? t.target : "—"}
              </dd>
              <dt>이름</dt>
              <dd>{v.draft.name ? v.draft.name : "—"}</dd>
              <dt>목표</dt>
              <dd>{v.draft.goal ? v.draft.goal : "비움 (엔진이 나눔)"}</dd>
              <dt>모델</dt>
              <dd>{picked ? `${picked.name || `프로필 #${picked.id}`} · ${picked.model}` : "엔진 활성 설정"}</dd>
              <dt>제외 경로</dt>
              <dd>{v.excl.length ? `${v.excl.length}개 (작업 규칙)` : "없음"}</dd>
              <dt>커버리지</dt>
              <dd>{f.coverage ? "기록" : "끔"}</dd>
            </dl>
          </div>
          <div className={s.isec}>
            <h3>엔진에 보낼 내용 · POST /api/tasks</h3>
            <pre className={s.code} id="lvBody">
              {JSON.stringify(preview, null, 2)}
            </pre>
            <p className={s.help}>비밀번호·세션 같은 비밀 값은 보내지 않습니다.</p>
          </div>
          <div className={s.isec}>
            <div className={cx(s.note, s.warn)} role="note">
              <Icon name="alert" />
              <div>엔진은 만들자마자 점검을 시작합니다(범위 확인 단계 없음).</div>
            </div>
            <button className={cx(s.btn, s.pri, s.full)} type="button" disabled={blocked} onClick={() => void start()}>
              {startLabel}
            </button>
            <span className={s.why}>{why}</span>
            {errBox}
          </div>
        </aside>
      </div>
    </>
  );
}
