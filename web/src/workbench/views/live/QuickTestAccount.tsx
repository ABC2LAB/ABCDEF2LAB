"use client";

// 새 점검 '계정 분류' 칸의 시험 계정 두 줄(사용자 11/24). 지금 확정 단계는 수평(같은 권한 두 계정)만이라 계정은 A(소유) ·
// B(교차) 딱 둘이면 된다 — 이름을 적어 하나씩 추가하는 상자 대신 두 줄을 고정으로 둔다. 계정 이름은 정책 선언의 소유 · 교차
// 계정 이름을 그대로 쓴다(확정 단계가 그 이름으로 계정을 찾는다). 선언이 없으면 이미 있는 시험 계정, 그것도 없으면 tester-a/b.
// [저장]은 아이디 · 비밀번호를 채운 줄만 등록 → 시험 표시 → 자격 등록을 차례로 부르고, 이미 된 단계는 건너뛴다.
// ※ 자격 칸은 React 상태에 두지 않는 비제어 입력이다. 누르자마자 DOM 칸에서 한 번 읽고 바로 비운다(보내지 않는 경우도). 값은
//   요청 본문에만 실리고 URL · 브라우저 저장소 · 로그 · 화면에 남지 않는다. 비밀번호 관리자에 안 들어가게 form 을 쓰지 않는다.
// ※ 시험 표시는 서버 진술에 동의해야만 한다(서버가 그 문장의 해시를 동의 기록으로 남김). 진술을 못 받으면 표시가 필요한 줄은 막는다.

import { type RefObject, useEffect, useId, useRef, useState } from "react";

import { cx } from "../../lib/cx";
import {
  accountWriteFailureText,
  type ClassifiedAccount,
  getAccounts,
  isValidAccountLabel,
  MIN_USABLE_PASSWORD_BYTES,
  markTestAccount,
  NO_SPACE_USERNAME,
  type ProductTargetRef,
  passwordTooShortForRun,
  registerAccount,
  setAccountCredential,
  unmarkAccount,
  usableAsTestAccount,
  validAccountCredential,
} from "../../live/accounts";
import s from "../../workbench.module.css";
import { TargetLoginForm } from "./TargetLoginForm";

/** 두 줄의 계정 이름: 선언의 소유 · 교차 → 이미 있는 시험 계정 → 기본 이름. 두 이름이 같아지지 않게 한다. */
export function pairLabels(rows: ClassifiedAccount[] | null, owner?: string, cross?: string): [string, string] {
  const tests = (rows ?? []).filter((a) => usableAsTestAccount(a.cls)).map((a) => a.ref);
  const a = owner ?? tests[0] ?? "tester-a";
  const b = cross ?? tests.find((r) => r !== a) ?? (a === "tester-b" ? "tester-a" : "tester-b");
  return [a, b];
}

type Slot = {
  key: "A" | "B";
  label: string;
  user: RefObject<HTMLInputElement | null>;
  pass: RefObject<HTMLInputElement | null>;
};

export function QuickTestAccount({
  target,
  owner,
  cross,
  revision = 0,
  onAdded,
}: {
  /** 승인된 대상. 없으면(승인 전) 칸은 보이고 저장만 막는다(사용자 11/24: 새 점검에서 늘 보이게) */
  target: ProductTargetRef | null;
  /** 바깥에서 계정이 바뀐 횟수(자세히 관리 · 새 점검 재진입). 바뀌면 상태를 다시 읽는다 */
  revision?: number;
  /** 정책 선언의 소유 · 교차 계정 이름(선언이 없으면 비움) */
  owner?: string;
  cross?: string;
  onAdded?: () => void;
}) {
  const uid = useId();
  const targetId = target?.id ?? null;
  const [rows, setRows] = useState<ClassifiedAccount[] | null>(null);
  const [statement, setStatement] = useState("");
  const [readFailed, setReadFailed] = useState(false);
  const [consent, setConsent] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [done, setDone] = useState("");
  const userA = useRef<HTMLInputElement>(null);
  const passA = useRef<HTMLInputElement>(null);
  const userB = useRef<HTMLInputElement>(null);
  const passB = useRef<HTMLInputElement>(null);
  // 지금 화면 대상. 늦게 온 이전 대상 응답은 버린다
  const current = useRef<string | null>(targetId);

  // 대상이 바뀌면 입력 · 안내를 비운다
  useEffect(() => {
    current.current = targetId;
    setRows(null);
    setStatement("");
    setConsent(false);
    setError("");
    setDone("");
  }, [targetId]);
  // 진술 문장과 지금 계정(상태 표시 · 있는 단계를 건너뛰려고). 대상 · 바깥 변경 때 다시 읽는다(안내는 그대로)
  // biome-ignore lint/correctness/useExhaustiveDependencies: revision 은 다시 읽는 트리거
  useEffect(() => {
    if (!targetId) return;
    let live = true;
    setReadFailed(false);
    getAccounts({ id: targetId, origin: "", state: "approved" })
      .then((r) => {
        if (!live) return;
        setRows(r.accounts);
        setStatement(r.statement);
      })
      .catch(() => {
        if (live) setReadFailed(true);
      });
    return () => {
      live = false;
    };
  }, [targetId, revision]);

  const [labelA, labelB] = pairLabels(rows, owner, cross);
  const slots: Slot[] = [
    // 줄 이름은 '계정 A · 계정 B'만(사용자 11/24: 소유 · 교차 글자는 헷갈려서 뺌 — A 가 만들고 B 가 읽는 역할은 정책 선언이 정함)
    { key: "A", label: labelA, user: userA, pass: passA },
    { key: "B", label: labelB, user: userB, pass: passB },
  ];
  const find = (label: string) => rows?.find((a) => a.ref === label);
  // 시험 표시가 필요한 줄(없거나 아직 시험 표시 안 됨)이 있으면 진술 동의가 있어야 저장한다
  const needsMark = slots.some((x) => !usableAsTestAccount(find(x.label)?.cls ?? "unmarked"));

  function save() {
    // 값은 여기서 한 번 읽고 바로 비운다(보내지 않는 경우도)
    const typed = slots.map((x) => {
      const username = x.user.current?.value ?? "";
      const password = x.pass.current?.value ?? "";
      if (x.user.current) x.user.current.value = "";
      if (x.pass.current) x.pass.current.value = "";
      return { ...x, username, password };
    });
    setError("");
    setDone("");
    if (busy || !rows || !targetId) return;
    const filled = typed.filter((x) => x.username || x.password);
    if (!filled.length) {
      setError("아이디 · 비밀번호를 넣으세요");
      return;
    }
    for (const x of filled) {
      if (!isValidAccountLabel(x.label) || !validAccountCredential(x.username, x.password)) {
        setError(`계정 ${x.key}: ${accountWriteFailureText(new Error("invalid_account_credential"))}`);
        return;
      }
      if (!usableAsTestAccount(find(x.label)?.cls ?? "unmarked") && !(statement && consent)) {
        setError("시험 계정 진술에 동의해야 저장합니다");
        return;
      }
    }
    // 바이트 수만 본다(값은 보지 않음) — 실행 때 E② 보호 값 하한보다 짧으면 등록은 되지만 그 계정으로 로그인하지 않는다
    const short = filled.filter((x) => passwordTooShortForRun(x.password)).map((x) => `계정 ${x.key}`);
    const id = targetId;
    setBusy(true);
    (async () => {
      for (const x of filled) {
        const existing = find(x.label);
        if (!existing) await registerAccount(id, x.label);
        if (!usableAsTestAccount(existing?.cls ?? "unmarked")) await markTestAccount(id, x.label);
        if (!(await setAccountCredential(id, x.label, x.username, x.password)))
          throw new Error("credential_not_confirmed");
      }
      return getAccounts({ id, origin: "", state: "approved" });
    })()
      .then((r) => {
        if (current.current !== id) return;
        setRows(r.accounts);
        setStatement(r.statement);
        setConsent(false);
        setDone(
          short.length
            ? `저장함 · ${short.join(" · ")}은 비밀번호가 ${MIN_USABLE_PASSWORD_BYTES}바이트보다 짧아 실행 때 로그인하지 않습니다`
            : "저장함",
        );
        onAdded?.();
      })
      .catch((e: unknown) => {
        if (current.current === id) setError(accountWriteFailureText(e));
        // 중간 단계까지 된 경우(등록만 · 표시까지) 상태를 맞추려고 다시 읽는다
        getAccounts({ id, origin: "", state: "approved" })
          .then((r) => {
            if (current.current === id) setRows(r.accounts);
          })
          .catch(() => undefined);
        onAdded?.();
      })
      .finally(() => setBusy(false));
  }

  // 줄 버튼: 시험 표시 해제 · (동의 뒤) 시험 표시. 자격 값은 보내지 않는다
  function act(kind: "unmark" | "mark", label: string) {
    if (busy || !rows || !targetId) return;
    if (kind === "mark" && !(statement && consent)) {
      setError("시험 계정 진술에 동의해야 표시합니다");
      return;
    }
    const id = targetId;
    setError("");
    setDone("");
    setBusy(true);
    (kind === "unmark" ? unmarkAccount(id, label) : markTestAccount(id, label))
      .then(() => getAccounts({ id, origin: "", state: "approved" }))
      .then((r) => {
        if (current.current !== id) return;
        setRows(r.accounts);
        setStatement(r.statement);
        if (kind === "mark") setConsent(false);
        onAdded?.();
      })
      .catch((e: unknown) => {
        if (current.current === id) setError(accountWriteFailureText(e));
      })
      .finally(() => setBusy(false));
  }

  let blocked: string | null = null;
  if (!targetId) blocked = "대상이 승인되면 저장할 수 있습니다";
  else if (readFailed) blocked = "계정 정보를 읽지 못했습니다";
  else if (rows && !statement && needsMark) blocked = "진술을 받지 못해 새 시험 계정은 저장할 수 없습니다";

  return (
    <section className={s["acct-quick"]} aria-label="시험 계정">
      {/* 로그인 창처럼 위에서부터: 대상 로그인 방식 → 시험 계정 A · B(사용자 11/24) */}
      <TargetLoginForm targetId={targetId} onSaved={onAdded} />
      <h3 className={s["acct-quick-h"]}>시험 계정</h3>
      {slots.map((x) => {
        const a = find(x.label);
        const test = !!a && usableAsTestAccount(a.cls);
        let state = targetId ? "확인 중" : "대상 승인 전";
        if (rows && !a) state = "등록 안 됨";
        else if (rows && !test) state = "시험 표시 안 됨";
        else if (rows) state = a?.credentialConfigured ? "자격 등록됨" : "자격 없음";
        return (
          // biome-ignore lint/a11y/useSemanticElements: 아이디 · 비밀번호 두 칸 묶음(fieldset 테두리 없이)
          <div className={s["acct-pair"]} role="group" aria-label={`계정 ${x.key}`} key={x.key}>
            <div className={s["acct-pair-h"]}>
              {/* 내부 별칭(vampi-a 등)은 서버 · 정책 선언이 계정을 가리키는 이름이라 화면엔 안 보이고 마우스를 올리면만(사용자 11/24) */}
              <b title={`별칭 ${x.label}`}>{`계정 ${x.key}`}</b>
              <span className={cx(s.tag, state === "자격 등록됨" ? s.in : s.ex)}>{state}</span>
              {/* 표시 해제 · 시험 표시도 이 상자에서(사용자 11/24) */}
              {test ? (
                <button
                  className={cx(s.btn, s.sm, s["acct-pair-act"])}
                  type="button"
                  disabled={busy}
                  onClick={() => act("unmark", x.label)}
                >
                  표시 해제
                </button>
              ) : null}
              {a && !test ? (
                <button
                  className={cx(s.btn, s.sm, s["acct-pair-act"])}
                  type="button"
                  disabled={busy || !(statement && consent)}
                  onClick={() => act("mark", x.label)}
                >
                  시험 표시
                </button>
              ) : null}
            </div>
            <div className={s["acct-pair-in"]}>
              <input
                ref={x.user}
                id={`${uid}-${x.key}-u`}
                className={s.inp}
                type="text"
                aria-label={`계정 ${x.key} 아이디`}
                placeholder="아이디"
                {...NO_SPACE_USERNAME}
                autoComplete="off"
                data-1p-ignore
                data-lpignore="true"
                data-form-type="other"
                spellCheck={false}
              />
              <input
                ref={x.pass}
                id={`${uid}-${x.key}-p`}
                className={s.inp}
                type="password"
                aria-label={`계정 ${x.key} 비밀번호`}
                placeholder={state === "자격 등록됨" ? "비밀번호 (새로 넣으면 바꿈)" : "비밀번호"}
                autoComplete="off"
                data-1p-ignore
                data-lpignore="true"
                data-form-type="other"
              />
            </div>
          </div>
        );
      })}
      {statement && needsMark ? (
        <label className={s.ckline}>
          <input type="checkbox" checked={consent} onChange={(e) => setConsent(e.target.checked)} disabled={busy} />
          <span>{statement}</span>
        </label>
      ) : null}
      {blocked ? <p className={s.empty}>{blocked}</p> : null}
      <button
        className={cx(s.btn, s.pri)}
        type="button"
        disabled={busy || !targetId || !rows || (needsMark && !(statement && consent))}
        onClick={save}
      >
        {busy ? "저장하는 중…" : "저장"}
      </button>
      {error ? (
        <p className={s.err} role="alert">
          {error}
        </p>
      ) : null}
      {done ? <p role="status">{done}</p> : null}
    </section>
  );
}
