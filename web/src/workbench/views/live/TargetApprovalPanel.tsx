"use client";

import { useCallback, useEffect, useRef, useState } from "react";

import { ApiError, apiFetch } from "@/lib/diagnosis/api";

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
  readonly_scope_required: "GET/HEAD 경로와 비변경 승인 근거를 구체적으로 등록하세요.",
  invalid_path_scope: "허용 경로 형식을 확인하세요. query·fragment·인코딩은 허용하지 않습니다.",
};
function originOf(raw: string): string {
  try {
    const u = new URL(raw);
    return u.protocol === "https:" && !u.username && !u.password ? u.origin : "";
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
  if (
    t.state === "approved" &&
    (!t.addresses?.length ||
      !/^[a-f0-9]{64}$/.test(t.tls_spki_sha256 ?? "") ||
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
    if (origin) {
      setPaths("");
      setBasis("");
    }
  }, [origin]);
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
        approve: "서버가 소유·범위·DNS/IP·TLS를 확인해 승인했습니다. 진단 범위 확인으로 계속하세요.",
        register: "등록은 실행 승인이 아닙니다. 아래 DNS TXT 레코드를 추가하고 소유 확인을 진행하세요.",
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
  return (
    <div className={s.fsec}>
      <div className={s["fsec-h"]}>
        <h2>대상 권한 · 운영자 승인</h2>
        <p>새 대상은 DNS 소유 확인과 구체적인 읽기 전용 범위 승인이 필요합니다.</p>
        <p className={s.help}>
          읽기 승인은 경로 단위이며 해당 경로의 임의 query까지 포함합니다. 서버는 query의 무해성을 검증하지 않습니다.
          query로 상태가 바뀔 수 있는 경로는 읽기 범위에 포함하지 마세요.
        </p>
        <p className={s.help}>
          소유 승인 시 후보 IP별 TLS 연결(최대 8회), 시작 직전 선택 IP로 TLS 연결 1회를 수행합니다. HTTP 진단은 별도
          시작 후 실행합니다.
        </p>
      </div>
      <div className={s["fsec-b"]}>
        {selected?.state === "approved" ? (
          <>
            <p>승인된 대상: {selected.origin}</p>
            <p className={s.help}>
              경로 {(selected.path_prefixes ?? []).join(", ")} · {(selected.methods ?? []).join("/")} · 회차 요청 상한{" "}
              {selected.requests}
            </p>
            <p className={s.help}>
              서버 확인 IP {(selected.addresses ?? []).join(", ")} · 포트 {selected.port} · TLS 공개키{" "}
              {selected.tls_spki_sha256?.slice(0, 16)}…
            </p>
            <p className={s.help}>승인 만료 {selected.expires_at}. 시작 전에 DNS·TLS와 승인 상태를 다시 확인합니다.</p>
            <button
              className={s.btn}
              type="button"
              disabled={disabled || busy || uncertain}
              onClick={() => void change("revoke")}
            >
              이 대상 승인 철회
            </button>
          </>
        ) : null}
        {selected?.state === "pending" ? (
          <>
            <p>DNS TXT 레코드 이름</p>
            <code id="targetChallengeName">{selected.challenge_name}</code>
            <p>DNS TXT 레코드 값</p>
            <code id="targetChallengeValue">{selected.challenge_value}</code>
            <p className={s.help}>
              대상 DNS 운영 화면에 레코드를 추가하세요. GET/HEAD도 상태를 바꿀 수 있으므로 등록한 경로의 비변경 승인이
              필요합니다.
            </p>
            <button
              className={s.btn}
              type="button"
              disabled={disabled || busy || uncertain}
              onClick={() => void change("approve")}
            >
              소유 확인 후 대상 승인
            </button>
          </>
        ) : null}
        {selected?.state !== "approved" && selected?.state !== "pending" ? (
          <>
            <div className={s.field}>
              <label className={s.lbl} htmlFor="targetReadonlyPaths">
                허용할 읽기 전용 경로 · 한 줄에 하나
              </label>
              <textarea
                id="targetReadonlyPaths"
                className={s.inp}
                value={paths}
                onChange={(e) => setPaths(e.target.value)}
                disabled={disabled || busy || uncertain}
                placeholder="비워 두면 입력 URL의 경로를 사용합니다"
              />
            </div>
            <div className={s.field}>
              <label className={s.lbl} htmlFor="targetReadonlyBasis">
                운영자의 진단 권한·비변경 승인 근거
              </label>
              <textarea
                id="targetReadonlyBasis"
                className={s.inp}
                value={basis}
                onChange={(e) => setBasis(e.target.value)}
                disabled={disabled || busy || uncertain}
                maxLength={1024}
                placeholder="승인된 업무·환경과 해당 GET/HEAD 경로가 상태를 바꾸지 않는 근거를 20자 이상 기재하세요"
              />
            </div>
            {allowStateChanges ? (
              <label className={s.ckline}>
                <input
                  id="targetReversible"
                  type="checkbox"
                  checked={reversible}
                  disabled={disabled || busy || uncertain}
                  onChange={(e) => setReversible(e.target.checked)}
                />
                <span>
                  이 대상은 복구 가능한 소유 시험환경이며 선택한 POST/PUT/PATCH 검사의 권한도 있습니다. 각 요청은 전송
                  전에 확인합니다. 삭제·초기화·결제 등 고위험 작업은 제외합니다.
                </span>
              </label>
            ) : null}
            <p className={s.help}>
              회차 요청 상한 {requestLimit} · 상태 변경 범위는 별도 동의한 설치에서만 등록할 수 있습니다.
            </p>
            <p className={s.help}>
              등록 후 DNS TXT 소유 증명을 확인합니다. 비변경 근거는 운영자의 확인이며 서버가 GET/HEAD의 무해성을 자동
              검증하지 않습니다.
            </p>
            <button
              className={s.btn}
              type="button"
              disabled={!origin || disabled || busy || uncertain || basis.trim().length < 20}
              onClick={() => void change("register")}
            >
              대상 등록·소유 확인 준비
            </button>
          </>
        ) : null}
        <button className={s.btn} type="button" disabled={busy} onClick={() => void list()}>
          대상 승인 목록 다시 조회
        </button>
        {message ? (
          <p role="status" className={s.help}>
            {message}
          </p>
        ) : null}
      </div>
    </div>
  );
}
