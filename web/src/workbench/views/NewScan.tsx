"use client";

// 새 점검: 한 장짜리 작업 시트 + 점검 요약. 원본 #nForm 과 nSync · nPrefill · vCheck · startScan.
// 입력값은 이 화면의 상태(useState)이고, 원본이 input·change 마다 nSync() 로 고치던 글·숨김·버튼 막기는 nView() 로 매 렌더 계산한다.
// 소유 확인 결과(VERIFIED)와 토큰(tokenFor)도 이 화면만 쓰므로 여기 둔다(화면은 늘 마운트돼 있어 값이 유지된다).

import { type ChangeEvent, type ReactNode, useEffect, useRef, useState } from "react";

import { cx } from "../lib/cx";
import {
  type Auth,
  DEPTHS,
  type Depth,
  GOALS,
  type How,
  initialForm,
  type Llm,
  MODELS,
  type NewForm,
  newToken,
  normalizeUrl,
  nView,
  PROVIDERS,
  prefillForm,
  type Send,
  syncName,
  type Verified,
  WORKERS,
} from "../lib/newScan";
import { useShell } from "../shell";
import { useWb } from "../store";
import { Icon } from "../ui/icons";
import { CloseInspButton, CopyButton, InspOpenButton, MenuButton } from "../ui/parts";
import s from "../workbench.module.css";

/** 최근 점검한 주소 (다시 쓰기 칩). */
const RECENT: ReadonlyArray<readonly [string, string, string]> = [
  ["https://blog.example.com", "blog.example.com", "10/25"],
  ["https://docs.example.com", "docs.example.com", "10/24"],
  ["https://app.sample.io", "app.sample.io", "10/23"],
];

/** 데이터를 바꾸는 요청 선택지 (값 · 제목 · 설명). */
const SEND_OPTS: ReadonlyArray<readonly [Send, string, string]> = [
  ["ap", "승인 후 전송 · 기본", "사람이 승인한 요청만 보냅니다"],
  ["full", "전체 허용", "승인 없이 보냅니다 · 시험환경 전용"],
];

/** 글자 입력 칸 (값이 그대로 폼 값인 것). */
type TextKey = "a1" | "a2" | "b1" | "b2" | "sa" | "sb" | "srv" | "excl" | "model" | "prov" | "workers";

/** `.seg` 라디오 묶음 (라벨 안에 input + span). */
function Seg<T extends string>({
  name,
  value,
  options,
  onPick,
  label,
  labelledBy,
}: {
  name: string;
  value: T;
  options: ReadonlyArray<readonly [T, ReactNode]>;
  onPick: (v: T) => void;
  label?: string;
  labelledBy?: string;
}) {
  return (
    <div className={s.seg} role="radiogroup" aria-label={label} aria-labelledby={labelledBy}>
      {options.map(([v, text]) => (
        <label key={v}>
          <input type="radio" name={name} value={v} checked={value === v} onChange={() => onPick(v)} />
          <span>{text}</span>
        </label>
      ))}
    </div>
  );
}

export function NewScan() {
  const { route, navSeq, goTo } = useShell();
  const prefill = useWb((st) => st.prefill);
  const startScan = useWb((st) => st.startScan);

  const [f, setF] = useState<NewForm>(initialForm);
  /** 원본 VERIFIED: 소유 확인(예시)을 통과한 hostname */
  const [verified, setVerified] = useState<Verified>({});
  /** 원본 tokenFor: hostname 별 TXT 토큰 (처음 외부 주소가 될 때 effect 에서 만든다) */
  const [tokens, setTokens] = useState<Readonly<Record<string, string>>>({});
  /** 확인 상자에 마지막으로 보인 외부 hostname (원본은 숨겨져도 마지막 값을 그대로 둠) */
  const [lastExt, setLastExt] = useState<string | null>(null);
  /** 'TXT 레코드 확인' 을 누른 hostname (1.3초 동안 버튼을 막는다) */
  const [checking, setChecking] = useState<string | null>(null);
  const [vState, setVState] = useState("토큰은 1시간 동안 이 주소에만 유효합니다.");
  const urlRef = useRef<HTMLInputElement>(null);
  const seenSeq = useRef(0);

  const v = nView(f, verified);
  const extHost = v.st.kind === "ext" ? v.st.url.hostname : null;
  const vh = extHost ?? lastExt ?? "example.com";
  const blocked = v.why.length > 0;
  const why = v.why.join(" · ");

  /** 입력 하나를 바꾸고 이름 채우기 규칙을 다시 적용 (원본: input·change 마다 nSync). */
  const set = (patch: Partial<NewForm>) => setF((p) => syncName({ ...p, ...patch }));
  const text = (k: TextKey) => ({
    value: f[k],
    onChange: (e: ChangeEvent<HTMLInputElement | HTMLTextAreaElement | HTMLSelectElement>) => {
      const val = e.target.value;
      setF((p) => syncName({ ...p, [k]: val }));
    },
  });

  // 화면에 들어올 때 다시 맞춘다 (원본 route('new') → nSync)
  useEffect(() => {
    if (route !== "new" || !navSeq) return;
    setF(syncName);
  }, [route, navSeq]);

  // 다른 화면의 '이 주소로 다시 점검' (원본 nPrefill). seq 마다 한 번.
  useEffect(() => {
    if (!prefill || prefill.seq === seenSeq.current) return;
    seenSeq.current = prefill.seq;
    setF((p) => prefillForm(p, prefill.url));
  }, [prefill]);

  // 처음 보는 외부 주소면 토큰을 만든다 (원본 nSync 의 tokenFor). 렌더 중에 난수를 쓰지 않으려고 effect 에서.
  useEffect(() => {
    if (!extHost) return;
    const tok = newToken();
    setTokens((t) => (t[extHost] ? t : { ...t, [extHost]: tok }));
    setLastExt(extHost);
  }, [extHost]);

  // 'TXT 레코드 확인': 시안이라 1.3초 뒤 예시로 통과시키고 주소 칸으로 포커스 (원본 vCheck)
  useEffect(() => {
    if (!checking) return;
    const t = window.setTimeout(() => {
      setVerified((m) => ({ ...m, [checking]: true }));
      setChecking(null);
      setVState("시안이라 실제 확인 없이 예시로 통과했습니다. 실제로는 DNS TXT 확인이 필요합니다.");
      urlRef.current?.focus();
    }, 1300);
    return () => window.clearTimeout(t);
  }, [checking]);

  const onCheck = () => {
    if (!v.st.url) return;
    setVState("시안 · 실제 DNS 조회는 하지 않습니다");
    setChecking(v.st.url.hostname);
  };

  /** 원본 startScan: 막혀 있으면 아무것도 안 하고, 아니면 대상 origin 으로 시작해 자산 탐색으로. */
  const start = () => {
    if (blocked || !v.st.url) return;
    startScan(v.st.url.origin);
    goTo("crawl");
  };

  const hl = v.hostLine;

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
          id="nForm"
          noValidate
          onSubmit={(e) => {
            e.preventDefault();
            start();
          }}
        >
          <div className={s.fsec}>
            <div className={s["fsec-h"]}>
              <h2>대상</h2>
              <p>허용 목록에 있는 주소나 내부 주소만 점검할 수 있습니다.</p>
            </div>
            <div className={s["fsec-b"]}>
              <div className={s.field}>
                <label className={s.lbl} htmlFor="nUrl">
                  점검할 사이트 주소
                </label>
                <input
                  ref={urlRef}
                  className={cx(s.inp, s.mono)}
                  id="nUrl"
                  type="url"
                  value={f.url}
                  autoComplete="url"
                  spellCheck={false}
                  aria-describedby="nHost"
                  aria-invalid={v.urlInvalid ? "true" : "false"}
                  onChange={(e) => set({ url: e.target.value })}
                  onBlur={(e) => {
                    const n = normalizeUrl(e.currentTarget.value);
                    if (n !== null) set({ url: n });
                  }}
                />
                <div className={s.hostline} id="nHost" aria-live="polite">
                  {hl.kind === "err" ? (
                    <span className={s.err}>{hl.text}</span>
                  ) : (
                    <>
                      <span className={cx(s.pill, s[hl.cls])}>{hl.pill}</span>
                      <span className={s.help}>{hl.help}</span>
                    </>
                  )}
                </div>
              </div>
              <div className={s.verify} id="nVerify" hidden={v.st.kind !== "ext"}>
                <div className={s.vt}>
                  <Icon name="alert" />
                  <div>
                    <b className={s.mono} id="vHost">
                      {vh}
                    </b>{" "}
                    은(는) 허용 목록에 없는 외부 주소입니다. 아래 TXT 레코드를 도메인 DNS에 추가한 뒤 확인을 누르세요.
                    웹 파일 인증은 우회 위험이 있어 지원하지 않습니다.
                  </div>
                </div>
                <div className={s.rec}>
                  <span className={s.k}>이름</span>
                  <code id="vName">_abc2lab-challenge.{vh}</code>
                  <CopyButton targetId="vName" />
                  <span className={s.k}>유형</span>
                  <code>TXT</code>
                  <span />
                  <span className={s.k}>값</span>
                  <code id="vToken">{tokens[vh] ?? "abc2lab-verify=…"}</code>
                  <CopyButton targetId="vToken" />
                </div>
                <div className={s.acts}>
                  <button
                    className={cx(s.btn, s.pri, s.sm)}
                    type="button"
                    id="vCheck"
                    disabled={checking !== null}
                    onClick={onCheck}
                  >
                    TXT 레코드 확인
                  </button>
                  <span className={s.help} id="vState">
                    {vState}
                  </span>
                </div>
              </div>
              <div className={s.field}>
                <span className={s.lbl}>최근 점검한 주소</span>
                <div className={s.chiprow}>
                  {RECENT.map(([url, host, date]) => (
                    <button
                      key={url}
                      className={s.rchip}
                      type="button"
                      onClick={() => {
                        // 원본 data-reuse: nPrefill 후 주소 칸으로 포커스
                        setF((p) => prefillForm(p, url));
                        urlRef.current?.focus();
                      }}
                    >
                      {host} <small>{date}</small>
                    </button>
                  ))}
                </div>
              </div>
            </div>
          </div>

          <div className={s.fsec}>
            <div className={s["fsec-h"]}>
              <h2>로그인</h2>
              <p>계정이 2개 이상이면 계정 사이의 접근(IDOR)도 점검합니다.</p>
            </div>
            <div className={s["fsec-b"]}>
              <Seg<Auth>
                name="nAuth"
                label="점검 영역"
                value={f.auth}
                options={[
                  ["in", "로그인 영역 포함"],
                  ["pub", "공개 영역만"],
                ]}
                onPick={(auth) => set({ auth })}
              />
              <div className={s["fsec-b"]} id="nAuthBox" hidden={f.auth !== "in"}>
                <Seg<How>
                  name="nHow"
                  label="로그인 방법"
                  value={f.how}
                  options={[
                    ["form", "아이디 · 비밀번호"],
                    ["sess", "세션 직접 입력"],
                  ]}
                  onPick={(how) => set({ how })}
                />
                <div className={s["fsec-b"]} id="nForm1" hidden={f.how !== "form"}>
                  <div className={s.acct}>
                    <span className={s.an}>계정 A</span>
                    <input
                      className={s.inp}
                      id="nA1"
                      placeholder="테스트 아이디"
                      aria-label="계정 A 아이디"
                      {...text("a1")}
                    />
                    <input
                      className={s.inp}
                      id="nA2"
                      type="password"
                      placeholder="비밀번호"
                      aria-label="계정 A 비밀번호"
                      autoComplete="new-password"
                      {...text("a2")}
                    />
                  </div>
                  <div className={s.acct}>
                    <span className={s.an}>계정 B</span>
                    <input
                      className={s.inp}
                      id="nB1"
                      placeholder="테스트 아이디"
                      aria-label="계정 B 아이디"
                      {...text("b1")}
                    />
                    <input
                      className={s.inp}
                      id="nB2"
                      type="password"
                      placeholder="비밀번호"
                      aria-label="계정 B 비밀번호"
                      autoComplete="new-password"
                      {...text("b2")}
                    />
                  </div>
                </div>
                <div className={s["fsec-b"]} id="nForm2" hidden={f.how !== "sess"}>
                  <div className={s.field}>
                    <label className={s.lbl} htmlFor="nSA">
                      계정 A 세션
                    </label>
                    <textarea className={s.inp} id="nSA" placeholder="Cookie: session=…" {...text("sa")} />
                  </div>
                  <div className={s.field}>
                    <label className={s.lbl} htmlFor="nSB">
                      계정 B 세션 · 계정 사이 접근 점검용
                    </label>
                    <textarea className={s.inp} id="nSB" placeholder="Cookie: session=…" {...text("sb")} />
                  </div>
                  <p className={s.help}>
                    시안입니다 — 실제 세션 값·비밀번호를 넣지 말고 예시 값만 쓰세요. (실제 연동 시에는 입력값을 보호하고
                    로그·내보내기에서 제외하도록 구현·검증해야 합니다. 이 시안에는 해당 보호 기능이 없습니다.)
                  </p>
                </div>
              </div>
            </div>
          </div>

          <div className={s.fsec}>
            <div className={s["fsec-h"]}>
              <h2>분석 모델</h2>
              <p>점검을 판단하는 LLM이 어디서 돌지 정합니다.</p>
            </div>
            <div className={s["fsec-b"]}>
              <Seg<Llm>
                name="nLlm"
                label="LLM 실행 위치"
                value={f.llm}
                options={[
                  ["local", "로컬 실행"],
                  ["api", "외부 API"],
                ]}
                onPick={(llm) => set({ llm })}
              />
              <div className={s.two} id="nLocal" hidden={f.llm !== "local"}>
                <div className={s.field}>
                  <label className={s.lbl} htmlFor="nModel">
                    모델
                  </label>
                  <select className={s.inp} id="nModel" {...text("model")}>
                    {MODELS.map((m) => (
                      <option key={m}>{m}</option>
                    ))}
                  </select>
                </div>
                <div className={s.field}>
                  <label className={s.lbl} htmlFor="nSrv">
                    LLM 서버 주소
                  </label>
                  <input className={cx(s.inp, s.mono)} id="nSrv" spellCheck={false} {...text("srv")} />
                </div>
              </div>
              <div className={s.hostline} id="nLocalState" hidden={f.llm !== "local"}>
                <span className={cx(s.pill, s.ok)}>내부 LLM 연결됨</span>
                <span className={s.help}>외부 API를 쓰지 않습니다</span>
              </div>
              <div className={s["fsec-b"]} id="nApi" hidden={f.llm !== "api"}>
                <div className={s.two}>
                  <div className={s.field}>
                    <label className={s.lbl} htmlFor="nProv">
                      제공자
                    </label>
                    <select className={s.inp} id="nProv" {...text("prov")}>
                      {PROVIDERS.map((p) => (
                        <option key={p}>{p}</option>
                      ))}
                    </select>
                  </div>
                  <div className={s.field}>
                    <span className={s.lbl}>API 키</span>
                    <div className={s.hostline}>
                      <span className={cx(s.pill, s.ok)}>등록됨 10/28</span>
                      <span className={s.help}>저장된 키는 다시 보여주지 않습니다</span>
                    </div>
                  </div>
                </div>
                <div className={cx(s.note, s.warn)}>
                  <Icon name="alert" />
                  <div>
                    대상 데이터와 프롬프트가 외부 제공자로 전송됩니다. 폐쇄망이나 민감한 환경에서는 로컬 실행을 쓰세요.
                  </div>
                </div>
              </div>
            </div>
          </div>

          <div className={s.fsec}>
            <div className={s["fsec-h"]}>
              <h2>작업</h2>
              <p>이름과 목표, 데이터를 바꾸는 요청을 어떻게 다룰지 정합니다.</p>
            </div>
            <div className={s["fsec-b"]}>
              <div className={s.field}>
                <label className={s.lbl} htmlFor="nName">
                  작업 이름<span className={s.req}>필수</span>
                </label>
                <input
                  className={s.inp}
                  id="nName"
                  value={f.name}
                  aria-describedby="nNameErr"
                  aria-invalid={v.nameOk ? "false" : "true"}
                  onChange={(e) => {
                    const name = e.target.value;
                    setF((p) => ({ ...p, name, nameTouched: true }));
                  }}
                />
                <span className={s.err} id="nNameErr" hidden={v.nameOk}>
                  작업 이름을 입력하세요.
                </span>
              </div>
              <div className={s.field}>
                <span className={s.lbl} id="nGoalL">
                  점검 목표 · 선택
                </span>
                {/* biome-ignore lint/a11y/useSemanticElements: 원본 마크업 그대로 (fieldset 으로 바꾸면 모양이 달라짐) */}
                <div className={s.goals} role="group" aria-labelledby="nGoalL">
                  {GOALS.map((g, i) => (
                    <label key={g} className={s.goal}>
                      <input
                        type="checkbox"
                        value={g}
                        checked={f.goals[i]}
                        onChange={(e) => {
                          const on = e.target.checked;
                          setF((p) => syncName({ ...p, goals: p.goals.map((x, j) => (j === i ? on : x)) }));
                        }}
                      />
                      <span>{g}</span>
                    </label>
                  ))}
                </div>
                <span className={s.help}>비워 두면 기본 목표로 점검합니다.</span>
              </div>
              <div className={s.field}>
                <span className={s.lbl} id="nSendL">
                  데이터를 바꾸는 요청 (결제 · 수정 · 삭제)
                </span>
                <div className={s.opts} role="radiogroup" aria-labelledby="nSendL">
                  {SEND_OPTS.map(([val, b, small]) => (
                    <label key={val} className={s.opt}>
                      <input
                        type="radio"
                        name="nSend"
                        value={val}
                        checked={f.send === val}
                        onChange={() => set({ send: val })}
                      />
                      <b>{b}</b>
                      <small>{small}</small>
                    </label>
                  ))}
                </div>
                <div className={cx(s.note, s.warn)} id="nFullWarn" hidden={f.send !== "full"}>
                  <Icon name="alert" />
                  <div style={{ display: "flex", flexDirection: "column", gap: 8 }}>
                    실제 주문이 만들어지거나 데이터가 지워질 수 있습니다. 운영 서버에는 쓰지 마세요. 전체 허용에서도
                    허용 목록, 제외 경로, 감사 로그는 그대로 적용됩니다.
                    <label className={s.ckline}>
                      <input
                        type="checkbox"
                        id="nReset"
                        checked={f.reset}
                        onChange={(e) => set({ reset: e.target.checked })}
                      />
                      이 대상은 리셋할 수 있는 시험환경입니다
                    </label>
                  </div>
                </div>
              </div>
              <div className={s.two}>
                <div className={s.field}>
                  <span className={s.lbl} id="nDepthL">
                    점검 범위
                  </span>
                  <Seg<Depth>
                    name="nDepth"
                    labelledBy="nDepthL"
                    value={f.depth}
                    options={DEPTHS.map((d) => [d, d] as const)}
                    onPick={(depth) => set({ depth })}
                  />
                  <span className={s.help} id="nDepthHelp">
                    {v.depthHelp}
                  </span>
                </div>
                <div className={s.field}>
                  <label className={s.lbl} htmlFor="nWorkers">
                    워커 수
                  </label>
                  <select className={s.inp} id="nWorkers" {...text("workers")}>
                    {WORKERS.map((w) => (
                      <option key={w}>{w}</option>
                    ))}
                  </select>
                  <span className={s.help}>LLM이 동시에 처리하는 작업 수. 대상 서버 요청 속도와는 관계없습니다.</span>
                </div>
              </div>
              <div className={s.field}>
                <label className={s.lbl} htmlFor="nExcl">
                  제외 경로 · 선택
                </label>
                <textarea
                  className={s.inp}
                  id="nExcl"
                  placeholder={"/api/admin/reset\n/api/users/*/delete"}
                  aria-describedby="nExclHelp"
                  {...text("excl")}
                />
                <span className={s.help} id="nExclHelp">
                  적은 경로는 전체 허용에서도 요청하지 않습니다. 공통 규칙 /admin/delete* 는 이미 적용됩니다.
                </span>
              </div>
            </div>
          </div>
          <div className={s.formbar}>
            <button className={cx(s.btn, s.pri)} type="submit" disabled={blocked}>
              점검 시작
            </button>
            <span className={s.why}>{why}</span>
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
              <dd className={s.mono} id="sTarget">
                {v.sum.target}
              </dd>
              <dt>인증</dt>
              <dd id="sAuth">{v.sum.auth}</dd>
              <dt>LLM</dt>
              <dd id="sLlm">{v.sum.llm}</dd>
              <dt>범위</dt>
              <dd id="sDepth">{v.sum.depth}</dd>
              <dt>목표</dt>
              <dd id="sGoal">{v.sum.goal}</dd>
              <dt>바꾸는 요청</dt>
              <dd id="sSend">{v.sum.send}</dd>
            </dl>
          </div>
          <div className={s.isec}>
            <h3>이 점검에 적용되는 안전 정책</h3>
            <ul className={s.policy}>
              <li>
                <Icon name="check" />
                <div>
                  허용 목록 주소만 요청<small id="pTarget">{v.pol.target}</small>
                </div>
              </li>
              <li>
                <Icon name="check" />
                <div>
                  데이터를 바꾸는 요청<small id="pSend">{v.pol.send}</small>
                </div>
              </li>
              <li>
                <Icon name="check" />
                <div>
                  제외 경로는 요청 안 함<small id="pExcl">{v.pol.excl}</small>
                </div>
              </li>
              <li>
                <Icon name="check" />
                <div>
                  모든 요청과 승인 결정 기록<small>감사 로그 · 운영자는 지울 수 없음</small>
                </div>
              </li>
            </ul>
          </div>
          <div className={s.isec}>
            <button className={cx(s.btn, s.pri, s.full)} type="button" disabled={blocked} onClick={start}>
              점검 시작
            </button>
            <span className={s.why}>{why}</span>
            <p className={s.help}>
              시작하면 먼저 자산 탐색으로 링크와 폼을 따라가며 경로를 모읍니다. 탐색은 조회(GET) 요청만 보냅니다.
            </p>
          </div>
        </aside>
      </div>
    </>
  );
}
