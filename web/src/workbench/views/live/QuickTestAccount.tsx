"use client";

// 새 점검 계정 칸의 시험 계정 두 줄(사용자 11/24). 지금 확정 단계는 수평(같은 권한 두 계정)만이라 계정은 A(소유) ·
// B(교차) 딱 둘이면 된다 — 이름을 적어 하나씩 추가하는 상자 대신 두 줄을 고정으로 둔다. 계정 이름은 정책 선언의 소유 · 교차
// 계정 이름을 그대로 쓴다(확정 단계가 그 이름으로 계정을 찾는다). 선언이 없으면 이미 있는 등록 계정, 그것도 없으면 tester-a/b.
// [저장]은 아이디 · 비밀번호를 채운 줄만 등록 → 자격 등록을 차례로 부르고, 이미 등록된 계정은 등록을 건너뛴다.
// ※ 자격 칸은 React 상태에 두지 않는 비제어 입력이다. 누르자마자 DOM 칸에서 한 번 읽고 바로 비운다(보내지 않는 경우도). 값은
//   요청 본문에만 실리고 URL · 브라우저 저장소 · 로그 · 화면에 남지 않는다. 비밀번호 관리자에 안 들어가게 form 을 쓰지 않는다.
// ※ 계정 분류 폐지(사용자 결정 2026-11-26): 시험 표시 · 해제 · 표시 진술은 없다. 시험 계정만 등록하라는 공지는 사용자 책임이고,
//   서버는 자격이 저장된 등록 계정을 회차에 쓴다(#967).

import { type RefObject, useEffect, useId, useRef, useState } from "react";

import { cx } from "../../lib/cx";
import {
  accountWriteFailureText,
  clearAccountCredential,
  getAccounts,
  isValidAccountLabel,
  MIN_USABLE_PASSWORD_BYTES,
  NO_SPACE_USERNAME,
  type ProductTargetRef,
  passwordTooShortForRun,
  registerAccount,
  setAccountCredential,
  type TargetAccount,
  validAccountCredential,
} from "../../live/accounts";
import s from "../../workbench.module.css";
import { TargetLoginForm } from "./TargetLoginForm";

/**
 * 두 줄의 계정 이름: 선언의 소유 · 교차 → 이미 있는 등록 계정(등록 순서) → 기본 이름. 두 이름이 같아지지 않게 한다.
 * 서버 계정 목록은 라벨순이라 그대로 쓰면 뒤에 등록한 계정(예: tester-z 뒤 tester-a)이 A 자리를 빼앗아, '계정 A'에 넣은 자격이
 * 다른 계정에 저장된다(#972 이동찬 P2). 그래서 등록 시각(registered_at) 순으로 정하고, 같거나 모르면 서버 순서를 그대로 둔다.
 */
export function pairLabels(rows: TargetAccount[] | null, owner?: string, cross?: string): [string, string] {
  const at = (a: TargetAccount) => a.registeredAt ?? Number.POSITIVE_INFINITY;
  const byRegistered = (x: TargetAccount, y: TargetAccount) => {
    if (at(x) === at(y)) return 0;
    return at(x) < at(y) ? -1 : 1;
  };
  // 정렬은 안정 정렬이라 같은 시각 · 모르는 시각끼리는 서버 순서가 남는다
  const known = [...(rows ?? [])].sort(byRegistered).map((a) => a.ref);
  const a = owner ?? known[0] ?? "tester-a";
  const b = cross ?? known.find((r) => r !== a) ?? (a === "tester-b" ? "tester-a" : "tester-b");
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
  const [rows, setRows] = useState<TargetAccount[] | null>(null);
  const [readFailed, setReadFailed] = useState(false);
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
    setError("");
    setDone("");
  }, [targetId]);
  // 지금 계정(상태 표시 · 이미 등록된 계정은 등록을 건너뛰려고). 대상 · 바깥 변경 때 다시 읽는다(안내는 그대로)
  // biome-ignore lint/correctness/useExhaustiveDependencies: revision 은 다시 읽는 트리거
  useEffect(() => {
    if (!targetId) return;
    let live = true;
    setReadFailed(false);
    getAccounts({ id: targetId, origin: "", state: "approved" })
      .then((r) => {
        if (live) setRows(r.accounts);
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
    }
    // 바이트 수만 본다(값은 보지 않음) — 실행 때 E② 보호 값 하한보다 짧으면 등록은 되지만 그 계정으로 로그인하지 않는다
    const short = filled.filter((x) => passwordTooShortForRun(x.password)).map((x) => `계정 ${x.key}`);
    const id = targetId;
    setBusy(true);
    (async () => {
      for (const x of filled) {
        const existing = find(x.label);
        if (!existing) await registerAccount(id, x.label);
        if (!(await setAccountCredential(id, x.label, x.username, x.password)))
          throw new Error("credential_not_confirmed");
      }
      return getAccounts({ id, origin: "", state: "approved" });
    })()
      .then((r) => {
        if (current.current !== id) return;
        setRows(r.accounts);
        setDone(
          short.length
            ? `저장함 · ${short.join(" · ")}은 비밀번호가 ${MIN_USABLE_PASSWORD_BYTES}바이트보다 짧아 실행 때 로그인하지 않습니다`
            : "저장함",
        );
        onAdded?.();
      })
      .catch((e: unknown) => {
        if (current.current === id) setError(accountWriteFailureText(e));
        // 중간 단계까지 된 경우(등록만) 상태를 맞추려고 다시 읽는다
        getAccounts({ id, origin: "", state: "approved" })
          .then((r) => {
            if (current.current === id) setRows(r.accounts);
          })
          .catch(() => undefined);
        onAdded?.();
      })
      .finally(() => setBusy(false));
  }

  // 줄 버튼: 그 계정만 자격 철회. 값은 보내지 않는다
  function clearOne(label: string) {
    if (busy || !rows || !targetId) return;
    const id = targetId;
    setError("");
    setDone("");
    setBusy(true);
    clearAccountCredential(id, label)
      .then((cleared) => {
        if (!cleared) throw new Error("credential_not_cleared");
        return getAccounts({ id, origin: "", state: "approved" });
      })
      .then((r) => {
        if (current.current !== id) return;
        setRows(r.accounts);
        setDone("자격을 지웠습니다");
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

  return (
    <section className={s["acct-quick"]} aria-label="시험 계정">
      {/* 로그인 창처럼 위에서부터: 대상 로그인 방식 → 시험 계정 A · B(사용자 11/24) */}
      <TargetLoginForm targetId={targetId} onSaved={onAdded} />
      <h3 className={s["acct-quick-h"]}>시험 계정</h3>
      {slots.map((x) => {
        const a = find(x.label);
        let state = targetId ? "확인 중" : "대상 승인 전";
        if (rows && !a) state = "등록 안 됨";
        else if (rows) state = a?.credentialConfigured ? "자격 등록됨" : "자격 없음";
        return (
          // biome-ignore lint/a11y/useSemanticElements: 아이디 · 비밀번호 두 칸 묶음(fieldset 테두리 없이)
          <div className={s["acct-pair"]} role="group" aria-label={`계정 ${x.key}`} key={x.key}>
            <div className={s["acct-pair-h"]}>
              {/* 내부 별칭(vampi-a 등)은 서버 · 정책 선언이 계정을 가리키는 이름이라 화면엔 안 보이고 마우스를 올리면만(사용자 11/24) */}
              <b title={`별칭 ${x.label}`}>{`계정 ${x.key}`}</b>
              <span className={cx(s.tag, state === "자격 등록됨" ? s.in : s.ex)}>{state}</span>
              {/* 그 계정만 자격 철회(#982 이동찬 P2, 예전 '표시 해제'가 하던 일) */}
              {state === "자격 등록됨" ? (
                <button
                  className={cx(s.btn, s.sm, s["acct-pair-act"])}
                  type="button"
                  disabled={busy}
                  onClick={() => clearOne(x.label)}
                >
                  자격 지우기
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
      {blocked ? <p className={s.empty}>{blocked}</p> : null}
      <button className={cx(s.btn, s.pri)} type="button" disabled={busy || !targetId || !rows} onClick={save}>
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
