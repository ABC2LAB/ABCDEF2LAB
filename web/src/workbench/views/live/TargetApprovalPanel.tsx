"use client";

import { useCallback, useEffect, useRef, useState } from "react";

import { ApiError, apiFetch } from "@/lib/diagnosis/api";

import { cx } from "../../lib/cx";
import { Icon } from "../../ui/icons";
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
const TARGET_REQUEST_LIMIT = 4;
const TARGET_APPROVAL_VALID_HOURS = 4;
const reasons: Record<string, string> = {
  dns_ownership_required: "IP 주소 직접 등록은 지원하지 않습니다. DNS 소유 확인이 가능한 호스트를 사용하세요.",
  target_approval_expired: "TLS 확인 중 승인 시간이 지났습니다. 현재 승인 상태를 다시 조회하세요.",
  ownership_not_verified: "DNS TXT 소유 증명이 아직 확인되지 않았습니다. 레코드를 등록한 후 다시 확인하세요.",
  ownership_challenge_expired: "소유 확인 시간이 지났습니다. 새 등록을 준비하세요.",
  target_address_denied: "사설·메타데이터·보호 주소는 승인할 수 없습니다. 설치된 보안 정책을 확인하세요.",
  target_port_denied: "설치 정책이 허용하지 않는 포트입니다.",
  target_tls_unverified: "대상의 TLS 인증서를 확인하지 못했습니다.",
  target_unreachable: "대상 주소에 연결하지 못했습니다.",
  readonly_scope_required: "GET/HEAD 경로와 비변경 승인 근거를 구체적으로 등록하세요.",
  invalid_path_scope: "허용 경로 형식을 확인하세요. query·fragment·인코딩은 허용하지 않습니다.",
};
function originOf(raw: string): string {
  try {
    const u = new URL(raw);
    return (u.protocol === "https:" || u.protocol === "http:") && !u.username && !u.password ? u.origin : "";
  } catch {
    return "";
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
  if (
    t.state === "approved" &&
    (!t.addresses?.length ||
      !pinValid ||
      !t.path_prefixes?.length ||
      !t.expires_at)
  )
    throw new Error("invalid_approval_response");
  return t;
}

// 대상 승인은 진단 초안과 독립적이다. 서버가 DNS 소유를 검증하며 URL·체크박스만으로 실행 권위가 생기지 않는다.
export function TargetApprovalPanel({
  url,
  disabled,
  onChanged,
  requestLimit = TARGET_REQUEST_LIMIT,
  allowStateChanges = false,
}: {
  url: string;
  disabled: boolean;
  onChanged?: () => void;
  requestLimit?: number;
  allowStateChanges?: boolean;
}) {
  const generation = useRef(0);
  const mutationLock = useRef(false);
  const [details, setDetails] = useState<Target | null>(null);
  const [paths, setPaths] = useState("");
  const [basis, setBasis] = useState("");
  const [reversible, setReversible] = useState(false);
  const [busy, setBusy] = useState(false);
  const [unsupported, setUnsupported] = useState(false);
  const [uncertain, setUncertain] = useState(false);
  const [message, setMessage] = useState("");
  // 등록 칸은 [대상 등록하기]를 눌러야 펼친다 (시안 v91 소유 확인 상자 모양 그대로, 평소엔 한 줄 + 버튼)
  const [open, setOpen] = useState(false);
  const firstField = useRef<HTMLTextAreaElement>(null);
  const origin = originOf(url);
  const selected = details?.origin === origin ? details : null;
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
    if (origin) {
      setPaths("");
      setBasis("");
    }
  }, [origin]);
  useEffect(() => {
    if (open) firstField.current?.focus();
  }, [open]);
  if (unsupported) return null;
  async function change(action: "register" | "approve" | "revoke") {
    if (disabled || busy || uncertain || mutationLock.current || !origin || (action !== "register" && !selected))
      return;
    mutationLock.current = true;
    const version = ++generation.current;
    setBusy(true);
    setMessage("");
    try {
      const path =
        action === "register"
          ? "/api/product/targets"
          : `/api/product/targets/${encodeURIComponent(selected?.id ?? "")}/${action}`;
      let prefixes = paths
        .split("\n")
        .map((p) => p.trim())
        .filter(Boolean);
      if (!prefixes.length) prefixes = [new URL(url).pathname || "/"];
      const body =
        action === "register"
          ? {
              url,
              path_prefixes: prefixes,
              methods: allowStateChanges && reversible ? ["GET", "HEAD", "POST", "PUT", "PATCH"] : ["GET", "HEAD"],
              ...(allowStateChanges && reversible ? { reversible_writes: true } : {}),
              readonly_basis: basis.trim(),
              requests: requestLimit,
              valid_hours: TARGET_APPROVAL_VALID_HOURS,
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
          <span className={cx(s.pill, s.ok)}>승인된 대상</span>
          <span className={s.help}>
            {`${(selected.path_prefixes ?? []).join(", ")} · ${(selected.methods ?? []).join("/")} · 회차 요청 ${selected.requests}회 · 만료 ${selected.expires_at}`}
          </span>
          <button className={cx(s.btn, s.sm)} type="button" disabled={off} onClick={() => void change("revoke")}>
            승인 철회
          </button>
        </div>
        <span className={s.help}>
          {`서버가 확인한 연결 · IP ${(selected.addresses ?? []).join(", ") || "—"} · 포트 ${selected.port} · TLS 공개키 ${selected.tls_spki_sha256 ? `${selected.tls_spki_sha256.slice(0, 16)}…` : "—"}`}
        </span>
        {tail}
      </div>
    );
  }

  // 미등록 · 소유 확인 대기: 시안 v91 의 '소유 확인 필요' 딱지 + 노란 TXT 상자
  return (
    <div className={s.tgt} id="targetApproval">
      <div className={s.hostline}>
        <span className={cx(s.pill, s.wait)}>소유 확인 필요</span>
      </div>
      <div className={s.verify}>
        <div className={s.vt}>
          <Icon name="alert" />
          <div>
            <b className={s.mono}>{host}</b> 은(는) 아직 승인되지 않은 대상입니다.{" "}
            {pending
              ? "아래 TXT 레코드를 도메인 DNS에 추가한 뒤 확인을 누르세요."
              : "대상을 등록하면 도메인 DNS에 추가할 TXT 레코드가 나옵니다."}
          </div>
        </div>
        {pending ? (
          <>
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
            <div className={s.two}>
              <div className={s.field}>
                <label className={s.lbl} htmlFor="targetReadonlyPaths">
                  허용할 읽기 경로 · 한 줄에 하나
                </label>
                <textarea
                  ref={firstField}
                  id="targetReadonlyPaths"
                  className={s.inp}
                  value={paths}
                  onChange={(e) => setPaths(e.target.value)}
                  disabled={off}
                  placeholder="비우면 입력한 주소의 경로"
                />
              </div>
              <div className={s.field}>
                <label className={s.lbl} htmlFor="targetReadonlyBasis">
                  진단 권한 · 비변경 근거
                </label>
                <textarea
                  id="targetReadonlyBasis"
                  className={s.inp}
                  value={basis}
                  onChange={(e) => setBasis(e.target.value)}
                  disabled={off}
                  maxLength={1024}
                  placeholder="승인된 업무 · 환경과 이 GET/HEAD 경로가 상태를 바꾸지 않는 근거 (20자 이상)"
                />
              </div>
            </div>
            <p className={s.help}>
              읽기 승인은 경로 단위이며 그 경로의 모든 query 를 포함합니다. 서버는 query 의 무해성을 검증하지 않으니,
              query 로 상태가 바뀔 수 있는 경로는 넣지 마세요. 승인할 때 후보 IP별 연결(https 대상은 TLS 확인)을 최대 8회 합니다.
            </p>
            <p className={s.help} id="targetHarmlessNote">
              비변경 근거는 운영자 확인입니다 · 서버는 GET/HEAD 의 무해성을 검증하지 않습니다 · 등록 후 DNS TXT 소유를
              확인합니다
              {requestLimit ? ` · 회차 요청 상한 ${requestLimit}` : ""} · 상태 변경 범위는 별도 동의한 설치에서만 등록할
              수 있습니다
            </p>
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
                  복구 가능한 소유 시험환경이고 POST/PUT/PATCH 검사 권한이 있음 · 사용자 승인 모드는 요청마다 전송
                  전에 확인, AI 자동 모드는 건별 확인 없이 보냄 · 삭제·초기화·결제는 제외
                </span>
              </label>
            ) : null}
            <div className={s.acts}>
              <button
                className={cx(s.btn, s.pri, s.sm)}
                type="button"
                disabled={off || basis.trim().length < 20}
                onClick={() => void change("register")}
              >
                대상 등록 · 소유 확인 준비
              </button>
              <button className={cx(s.btn, s.sm)} type="button" disabled={busy} onClick={() => setOpen(false)}>
                접기
              </button>
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
