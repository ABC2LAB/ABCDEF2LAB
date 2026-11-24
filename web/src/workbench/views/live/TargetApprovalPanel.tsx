"use client";

import { useCallback, useEffect, useRef, useState } from "react";

import { ApiError, apiFetch } from "@/lib/diagnosis/api";

import { cx } from "../../lib/cx";
import { dateTimeOf } from "../../live/map";
import { Icon } from "../../ui/icons";
import { HelpTip } from "../../ui/parts";
import s from "../../workbench.module.css";

type Target = {
  id: string;
  origin: string;
  state: "pending" | "approved" | "revoked";
  path_prefixes?: string[] | null;
  methods?: string[] | null;
  requests: number;
  challenge_name?: string;
  challenge_value?: string;
  addresses?: string[];
  port: number;
  tls_spki_sha256?: string;
  expires_at?: string;
};
type Reply = { targets?: Target[]; target?: Target; reason?: string };
/**
 * 주소 옆 인증 칸(새 점검 'URL 인증')이 쓰는 이 주소의 대상 승인 상태.
 * invalid: 쓸 수 있는 http(s) 주소가 아님 · loading: 서버에 묻는 중 · uncertain: 확인 실패(다시 조회 필요) ·
 * unsupported: 서버에 대상 승인 API 가 없음 · none: 등록 안 됨 · 그 밖은 서버 대상 상태 그대로
 */
export type TargetStatus = "invalid" | "loading" | "uncertain" | "unsupported" | "none" | Target["state"];
/**
 * 대상 승인 유효 시간(승인 시각부터, 시간). 등록 칸에서 고르지 않고(사용자 11/24) 대상 승인 계약(product-target-approval-proposal.md)의
 * UI 기본값 4시간으로 보낸다. 서버 상한 `diagnosisprep.MaxTargetApprovalHours`(720) 안. 승인 범위(어디로 · 무엇을)는 바꾸지 않는다.
 */
export const TARGET_APPROVAL_HOURS = 4;
/**
 * 점검 근거(서버 readonly_basis): 계약상 운영자 진술이고 서버는 길이(20자 이상)만 본다. 글 칸 대신(사용자 11/24) 운영자가 이 문장에
 * 체크해야만 진술로 보낸다 — 체크 없이 프로그램이 대신 채우지 않는다.
 */
export const TARGET_READONLY_STATEMENT =
  "이 대상의 점검 권한이 있고, 승인 경로는 조회(GET · HEAD)해도 데이터가 바뀌지 않습니다";
const reasons: Record<string, string> = {
  dns_ownership_required: "IP 주소 직접 등록은 지원하지 않습니다. DNS 소유 확인이 가능한 호스트를 사용하세요.",
  target_approval_expired: "TLS 확인 중 승인 시간이 지났습니다. 현재 승인 상태를 다시 조회하세요.",
  ownership_not_verified: "DNS TXT 소유 증명이 아직 확인되지 않았습니다. 레코드를 등록한 후 다시 확인하세요.",
  ownership_challenge_expired: "소유 확인 시간이 지났습니다. 새 등록을 준비하세요.",
  target_address_denied: "사설·메타데이터·보호 주소는 승인할 수 없습니다. 설치된 보안 정책을 확인하세요.",
  target_port_denied: "설치 정책이 허용하지 않는 포트입니다.",
  target_tls_unverified: "대상의 TLS 인증서를 확인하지 못했습니다.",
  target_unreachable: "대상 주소에 연결하지 못했습니다. 주소·포트와 대상이 켜져 있는지 확인하세요.",
  readonly_scope_required: "GET/HEAD 경로와 비변경 승인 근거를 구체적으로 등록하세요.",
  invalid_path_scope: "허용 경로 형식을 확인하세요. query·fragment·인코딩은 허용하지 않습니다.",
};
/** http 대상(C22) 고지. 평문이라 요청·응답과 시험 계정 세션이 네트워크에서 보이고, 서버 신원을 TLS로 확인하지 않는다. */
export const PLAIN_TARGET_WARNING =
  "암호화하지 않는 주소입니다 · 요청·응답(로그인 세션 포함)이 네트워크에서 그대로 보일 수 있습니다 · 서버 신원(TLS)을 확인하지 않습니다";

function originOf(raw: string): string {
  try {
    const u = new URL(raw);
    return (u.protocol === "https:" || u.protocol === "http:") && !u.username && !u.password ? u.origin : "";
  } catch {
    return "";
  }
}
/** 등록할 승인 경로: 입력한 주소의 경로(없으면 /) */
function pathOf(raw: string): string {
  try {
    return new URL(raw).pathname || "/";
  } catch {
    return "/";
  }
}
function targetReply(value: Reply, origin: string): Target {
  const t = value.target;
  if (
    !t ||
    !/^target-[a-f0-9]{32}$/.test(t.id) ||
    originOf(t.origin) !== t.origin ||
    t.origin !== origin ||
    !["pending", "approved", "revoked"].includes(t.state)
  )
    throw new Error("invalid_target_response");
  // https 대상은 TLS 공개키 고정값이 있어야 하고, http 대상(C22)은 TLS가 없어 고정값이 비어 있어야 한다.
  const pinValid = t.origin.startsWith("http://")
    ? (t.tls_spki_sha256 ?? "") === ""
    : /^[a-f0-9]{64}$/.test(t.tls_spki_sha256 ?? "");
  if (t.state === "approved" && (!t.addresses?.length || !pinValid || !t.path_prefixes?.length || !t.expires_at))
    throw new Error("invalid_approval_response");
  return t;
}

/** 주소 옆 인증 칸에 알릴 상태. 서버 답을 받기 전에는 '등록 안 됨'으로 말하지 않는다 */
function targetStatus(
  origin: string,
  unsupported: boolean,
  uncertain: boolean,
  resolved: boolean,
  state: Target["state"] | undefined,
): TargetStatus {
  if (!origin) return "invalid";
  if (unsupported) return "unsupported";
  if (uncertain) return "uncertain";
  if (!resolved) return "loading";
  return state ?? "none";
}

// 대상 승인은 진단 초안과 독립적이다. 서버가 DNS 소유를 검증하며 URL·체크박스만으로 실행 권위가 생기지 않는다.
export function TargetApprovalPanel({
  url,
  disabled,
  onChanged,
  requestLimit,
  allowStateChanges = false,
  onTarget,
  openSignal,
  onStatus,
  onOpenChange,
}: {
  url: string;
  disabled: boolean;
  onChanged?: () => void;
  // 이 주소의 서버 확인 대상(id · 상태). 없거나 모르면 null. 시작 화면이 확정 단계 안내 · 리셋 확인을 붙일 때 쓴다(E③)
  onTarget?: (target: { id: string; state: Target["state"] } | null) => void;
  // 설치의 회차당 요청 상한(서버 프로필 workflow_request_limit, 모드와 상관없음). 없으면 등록하지 않는다.
  requestLimit?: number;
  allowStateChanges?: boolean;
  /**
   * 주소 옆 인증 칸에서 여는 모드(새 점검). 값이 있으면 상자 머리 딱지와 접힌 상자([대상 등록하기])를 두지 않고,
   * 값이 바뀔 때마다 등록 칸을 펼친다. 상태 딱지는 부모가 주소 옆에 onStatus 로 그린다.
   */
  openSignal?: number;
  onStatus?: (status: TargetStatus) => void;
  /** 등록 칸이 펼쳐졌는지(주소 옆 [URL 인증]의 aria-expanded) */
  onOpenChange?: (open: boolean) => void;
}) {
  const inline = openSignal !== undefined;
  const generation = useRef(0);
  const mutationLock = useRef(false);
  const [details, setDetails] = useState<Target | null>(null);
  // 점검 근거 진술 체크(TARGET_READONLY_STATEMENT)
  const [attested, setAttested] = useState(false);
  const [reversible, setReversible] = useState(false);
  const [busy, setBusy] = useState(false);
  const [unsupported, setUnsupported] = useState(false);
  const [uncertain, setUncertain] = useState(false);
  const [message, setMessage] = useState("");
  // 등록 칸은 [대상 등록하기]를 눌러야 펼친다 (시안 v91 소유 확인 상자 모양 그대로, 평소엔 한 줄 + 버튼)
  const [open, setOpen] = useState(false);
  // 이 주소로 서버에 한 번이라도 물어 답을 받았는지(받기 전엔 '등록 안 됨'으로 보이지 않게)
  const [resolved, setResolved] = useState(false);
  const firstField = useRef<HTMLInputElement>(null);
  const origin = originOf(url);
  const approvePath = pathOf(url);
  const selected = details?.origin === origin ? details : null;
  const selectedId = selected?.id ?? null;
  const selectedState = selected?.state ?? null;
  useEffect(() => {
    onTarget?.(selectedId && selectedState ? { id: selectedId, state: selectedState } : null);
  }, [onTarget, selectedId, selectedState]);
  const list = useCallback(async () => {
    const version = ++generation.current;
    if (!origin) return;
    try {
      const r = await apiFetch<Reply>(
        `/api/product/targets/resolve?url=${encodeURIComponent(url)}`,
        { cache: "no-store", redirect: "error" },
        false,
      );
      if (version !== generation.current) return;
      if (!r || (!r.target && !Array.isArray(r.targets) && Object.keys(r).length !== 0))
        throw new Error("invalid_target_catalog");
      setDetails(r.target ? targetReply(r, origin) : null);
      setResolved(true);
      setUncertain(false);
      setMessage("");
    } catch (e) {
      if (version !== generation.current) return;
      if (e instanceof ApiError && [404, 501].includes(e.status)) setUnsupported(true);
      else {
        setUncertain(true);
        setMessage("대상 승인을 확인하지 못했습니다. 대상 목록을 다시 조회해 주세요.");
      }
    }
  }, [url, origin]);
  useEffect(() => {
    setDetails(null);
    setResolved(false);
    setBusy(false);
    mutationLock.current = false;
    setUncertain(false);
    setUnsupported(false);
    setMessage("");
    void list();
    return () => {
      generation.current++;
    };
  }, [list]);
  useEffect(() => {
    setReversible(false);
    setOpen(false);
    if (origin) setAttested(false);
  }, [origin]);
  useEffect(() => {
    if (open) firstField.current?.focus();
  }, [open]);
  // 주소 옆 [URL 인증]: 누를 때마다 등록 칸을 열고 닫는다(접기 버튼 대신, 사용자 11/24). 열 때 승인 상태를 확인하지 못한 채면
  // 한 번 더 묻는다 — 그동안 등록 칸은 열리되 등록 버튼은 꺼져 있다(off). 모르는 채 등록하면 같은 주소 대상이 겹친다
  const handledSignal = useRef(0);
  useEffect(() => {
    if (!openSignal || openSignal === handledSignal.current) return;
    handledSignal.current = openSignal;
    if (open) {
      setOpen(false);
      return;
    }
    setOpen(true);
    firstField.current?.focus();
    if (uncertain) void list();
  }, [openSignal, open, uncertain, list]);
  useEffect(() => {
    onOpenChange?.(open);
  }, [onOpenChange, open]);
  const status = targetStatus(origin, unsupported, uncertain, resolved, selected?.state);
  useEffect(() => {
    onStatus?.(status);
  }, [onStatus, status]);
  if (unsupported) return null;
  async function change(action: "register" | "approve" | "revoke") {
    if (disabled || busy || uncertain || mutationLock.current || !origin || (action !== "register" && !selected))
      return;
    // 점검 근거는 운영자가 체크한 진술만 보낸다
    if (action === "register" && !attested) return;
    mutationLock.current = true;
    const version = ++generation.current;
    setBusy(true);
    setMessage("");
    try {
      const path =
        action === "register"
          ? "/api/product/targets"
          : `/api/product/targets/${encodeURIComponent(selected?.id ?? "")}/${action}`;
      // 승인 경로는 입력한 주소의 경로(경로 칸은 뺐다, 사용자 11/24). 좁히려면 주소를 그 경로까지 넣는다
      const prefixes = [approvePath];
      const body =
        action === "register"
          ? {
              url,
              path_prefixes: prefixes,
              methods: allowStateChanges && reversible ? ["GET", "HEAD", "POST", "PUT", "PATCH"] : ["GET", "HEAD"],
              ...(allowStateChanges && reversible ? { reversible_writes: true } : {}),
              readonly_basis: TARGET_READONLY_STATEMENT,
              requests: requestLimit,
              valid_hours: TARGET_APPROVAL_HOURS,
            }
          : {};
      const r = await apiFetch<Reply>(path, { method: "POST", redirect: "error", body: JSON.stringify(body) }, false);
      if (version !== generation.current) return;
      if (r.reason) throw new Error(r.reason);
      const target = targetReply(r, origin);
      const expectedState = { register: "pending", approve: "approved", revoke: "revoked" }[action];
      if (target.state !== expectedState || (action !== "register" && target.id !== selected?.id))
        throw new Error("invalid_target_response");
      setDetails(target);
      onChanged?.();

      const messages = {
        approve: "서버가 소유 · 범위 · DNS · TLS 를 확인해 승인했습니다.",
        register: "등록했습니다. 아직 실행 승인이 아닙니다.",
        revoke: "승인을 철회했습니다. 새 실행은 차단됩니다.",
      };
      setMessage(messages[action]);
    } catch (e) {
      if (version !== generation.current) return;
      // 서버의 고정 사유만 표시한다. 응답·자격 원문과 결과 불명인 결정을 재사용하지 않는다.
      const code = e instanceof Error ? e.message : "";
      const known = Object.hasOwn(reasons, code);
      setUncertain(!known);
      setMessage(
        known ? reasons[code] : "변경 결과를 확인하지 못했습니다. 자동 재전송하지 말고 대상 목록을 다시 조회하세요.",
      );
    } finally {
      if (version === generation.current) {
        mutationLock.current = false;
        setBusy(false);
      }
    }
  }
  if (!origin) return null;
  const off = disabled || busy || uncertain;
  const host = new URL(origin).hostname;
  const pending = selected?.state === "pending" ? selected : null;
  // 결과 글 · 다시 조회 (어느 상태든 같은 자리: 상자 안 맨 아래)
  const tail = (
    <>
      {message ? (
        <p role="status" className={s.help}>
          {message}
        </p>
      ) : null}
      {uncertain ? (
        <div>
          <button className={cx(s.btn, s.sm)} type="button" disabled={busy} onClick={() => void list()}>
            대상 승인 다시 조회
          </button>
        </div>
      ) : null}
    </>
  );

  // 승인된 대상: 시안처럼 상자 없이 주소 밑 한 줄
  if (selected?.state === "approved") {
    return (
      <div className={cx(s.tgt, s.field)} id="targetApproval">
        <div className={s.hostline}>
          {inline ? null : <span className={cx(s.pill, s.ok)}>승인된 대상</span>}
          <span className={s.help}>
            {`${(selected.path_prefixes ?? []).join(", ")} · ${(selected.methods ?? []).join("/")} · 회차당 요청 상한 ${selected.requests} · 만료 ${dateTimeOf(selected.expires_at) || selected.expires_at}`}
          </span>
          <button className={cx(s.btn, s.sm)} type="button" disabled={off} onClick={() => void change("revoke")}>
            승인 철회
          </button>
        </div>
        <span className={s.help}>
          {`서버가 확인한 연결 · IP ${(selected.addresses ?? []).join(", ") || "—"} · 포트 ${selected.port} · ${
            selected.origin.startsWith("http://")
              ? "TLS 없음(http)"
              : `TLS 공개키 ${selected.tls_spki_sha256 ? `${selected.tls_spki_sha256.slice(0, 16)}…` : "—"}`
          }`}
        </span>
        {selected.origin.startsWith("http://") ? (
          <p className={cx(s.note, s.warn)} role="note">
            {PLAIN_TARGET_WARNING}
          </p>
        ) : null}
        {tail}
      </div>
    );
  }

  // 주소 옆 인증 칸 모드에서 등록 전 · 접힘: 상자 없이 결과 글 · 다시 조회만(있을 때)
  if (inline && !pending && !open)
    return message || uncertain ? (
      <div className={cx(s.tgt, s.field)} id="targetApproval">
        {tail}
      </div>
    ) : null;

  // 미등록 · 소유 확인 대기: 시안 v91 의 '소유 확인 필요' 딱지 + 노란 TXT 상자
  return (
    <div className={s.tgt} id="targetApproval">
      {inline ? null : (
        <div className={s.hostline}>
          <span className={cx(s.pill, s.wait)}>소유 확인 필요</span>
        </div>
      )}
      <div className={s.verify}>
        <div className={s.vt}>
          <Icon name="alert" />
          <div>
            {/* 서버 답을 못 받았으면 '승인 안 됨'으로 단정하지 않는다(규칙 5) */}
            {uncertain && !pending ? (
              <>
                <b className={s.mono}>{host}</b> 의 승인 상태를 확인하지 못했습니다. 다시 조회해 확인되면 등록할 수
                있습니다.
              </>
            ) : (
              <>
                <b className={s.mono}>{host}</b> 은(는) 아직 승인되지 않은 대상입니다.{" "}
                {pending
                  ? "아래 TXT 레코드를 도메인 DNS에 추가한 뒤 확인을 누르세요."
                  : "대상을 등록하면 도메인 DNS에 추가할 TXT 레코드가 나옵니다."}
              </>
            )}
          </div>
        </div>
        {pending ? (
          <>
            <p className={s.help} id="targetPendingRequestLimit">{`회차당 요청 상한 ${pending.requests}`}</p>
            <div className={s.rec}>
              <span className={s.k}>이름</span>
              <code id="targetChallengeName">{pending.challenge_name}</code>
              <CopyButton text={pending.challenge_name ?? ""} label="이름 복사" />
              <span className={s.k}>유형</span>
              <code>TXT</code>
              <span />
              <span className={s.k}>값</span>
              <code id="targetChallengeValue">{pending.challenge_value}</code>
              <CopyButton text={pending.challenge_value ?? ""} label="값 복사" />
            </div>
            <div className={s.acts}>
              <button
                className={cx(s.btn, s.pri, s.sm)}
                type="button"
                disabled={off}
                onClick={() => void change("approve")}
              >
                TXT 레코드 확인
              </button>
            </div>
          </>
        ) : null}
        {!pending && open ? (
          <div className={s["fsec-b"]} id="targetRegister">
            <p className={s.help} id="targetScopeLine">
              승인 경로 <code>{approvePath}</code> 아래
            </p>
            {/* 점검 근거: 운영자 진술 체크 한 줄 + 원래 고지 문장은 (?) 메모에(글 칸 · 경로 칸은 뺐다, 사용자 11/24) */}
            <label className={cx(s.ckline, s.tipline)} id="targetHarmlessNote">
              <input
                ref={firstField}
                id="targetAttest"
                type="checkbox"
                checked={attested}
                disabled={off}
                onChange={(e) => setAttested(e.target.checked)}
              />
              <span>
                {TARGET_READONLY_STATEMENT}{" "}
                <HelpTip label="읽기 승인 설명 보기">
                  위 경로 아래 전체와 모든 query 를 조회 승인합니다. 서버는 query 의 무해성을 검증하지 않으니, 조회로
                  상태가 바뀔 수 있는 경로가 있으면 주소를 그보다 좁게 넣으세요. 승인할 때 후보 IP별 연결(https 대상은
                  TLS 확인)을 최대 32회 합니다.
                  <br />
                  <br />이 확인은 운영자 진술입니다 · 서버는 GET/HEAD 의 무해성을 검증하지 않습니다 · 등록 후 DNS TXT
                  소유를 확인합니다 · 승인은 {TARGET_APPROVAL_HOURS}시간 동안 유효합니다
                  {requestLimit ? ` · 회차당 요청 상한 ${requestLimit}` : ""} · 상태 변경 범위는 별도 동의한 설치에서만
                  등록할 수 있습니다
                </HelpTip>
              </span>
            </label>
            {origin.startsWith("http://") ? (
              <p className={cx(s.note, s.warn)} role="note">
                {PLAIN_TARGET_WARNING}
              </p>
            ) : null}
            {allowStateChanges ? (
              <label className={s.ckline}>
                <input
                  id="targetReversible"
                  type="checkbox"
                  checked={reversible}
                  disabled={off}
                  onChange={(e) => setReversible(e.target.checked)}
                />
                <span>
                  복구 가능한 소유 시험환경이고 POST/PUT/PATCH 검사 권한이 있음 · 사용자 승인 모드는 요청마다 전송 전에
                  확인, AI 자동 모드는 건별 확인 없이 보냄 · 어떤 쓰기 요청이 나갈지 미리 모두 알 수 없고 대상 데이터나
                  상태가 바뀔 수 있음 · 정책 선언이 있으면 확정 단계에서 프로그램이 소유자 전용 종류마다 시험 객체를
                  만듦 · DELETE 요청은 보내지 않음 · 초기화·결제처럼 상태를 크게 바꾸는 경로는 점검 시작 때 제외 경로에
                  넣어야 막힘
                </span>
              </label>
            ) : null}
            {/* 회차당 요청 상한 줄은 뺐다(사용자 11/18) — 상한을 못 받아 등록할 수 없을 때만 이유를 보인다 */}
            {requestLimit ? null : (
              <p className={s.help} id="targetRequestLimit">
                설치의 회차당 요청 상한을 받지 못해 대상을 등록할 수 없습니다
              </p>
            )}
            <div className={s.acts}>
              <button
                className={cx(s.btn, s.pri, s.sm)}
                type="button"
                disabled={off || !attested || !requestLimit}
                onClick={() => void change("register")}
              >
                대상 등록
              </button>
              {/* 주소 옆 인증 칸 모드는 [URL 인증]을 다시 눌러 닫는다 */}
              {inline ? null : (
                <button className={cx(s.btn, s.sm)} type="button" disabled={busy} onClick={() => setOpen(false)}>
                  접기
                </button>
              )}
            </div>
          </div>
        ) : null}
        {!pending && !open ? (
          <div className={s.acts}>
            <button className={cx(s.btn, s.pri, s.sm)} type="button" disabled={off} onClick={() => setOpen(true)}>
              대상 등록하기
            </button>
          </div>
        ) : null}
        {tail}
      </div>
    </div>
  );
}

// TXT 레코드 이름 · 값 복사 (시안 data-copy). 클립보드를 못 쓰면 그 글을 선택해 둔다.
function CopyButton({ text, label }: { text: string; label: string }) {
  const [done, setDone] = useState(false);
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  useEffect(() => () => clearTimeout(timer.current), []);
  return (
    <button
      className={cx(s.btn, s.sm)}
      type="button"
      aria-label={label}
      onClick={(e) => {
        const code = e.currentTarget.previousElementSibling;
        const pick = () => {
          if (!code) return;
          const r = document.createRange();
          r.selectNodeContents(code);
          const sel = window.getSelection();
          sel?.removeAllRanges();
          sel?.addRange(r);
        };
        try {
          void navigator.clipboard
            .writeText(text)
            .then(() => {
              setDone(true);
              clearTimeout(timer.current);
              timer.current = setTimeout(() => setDone(false), 1500);
            })
            .catch(pick);
        } catch {
          pick();
        }
      }}
    >
      {done ? "복사됨" : "복사"}
    </button>
  );
}
