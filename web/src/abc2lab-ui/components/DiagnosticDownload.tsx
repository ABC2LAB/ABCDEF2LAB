import { useEffect, useRef, useState } from "react";

import { apiFetchBlob } from "../lib/api";

export function DiagnosticDownload({ taskId, enabled }: { taskId: string; enabled: boolean }) {
  const [audience, setAudience] = useState("shared");
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState("");
  const request = useRef<AbortController | null>(null);
  const validTask = /^[1-9][0-9]*$/.test(taskId);

  useEffect(() => {
    if (!enabled) {
      request.current?.abort();
      request.current = null;
      setBusy(false);
      setMessage("");
      setAudience("shared");
    }
    return () => {
      request.current?.abort();
      request.current = null;
    };
  }, [enabled]);

  async function download() {
    if (!enabled || !validTask || request.current) return;
    const controller = new AbortController();
    request.current = controller;
    setBusy(true);
    setMessage("");
    try {
      const query = new URLSearchParams({ task: taskId, audience, download: "1" });
      const blob = await apiFetchBlob(`/api/report/diagnostic?${query}`, { signal: controller.signal });
      if (controller.signal.aborted) return;
      const url = URL.createObjectURL(blob);
      const link = document.createElement("a");
      link.href = url;
      link.download = `diagnostic-task-${taskId}-${audience}.html`;
      document.body.appendChild(link);
      try {
        link.click();
        setMessage("다운로드를 요청했어요. 브라우저의 다운로드 목록을 확인하세요.");
      } finally {
        link.remove();
        // Give the browser time to consume the object URL; never open a preview tab.
        setTimeout(() => URL.revokeObjectURL(url), 1000);
      }
    } catch {
      if (!controller.signal.aborted)
        setMessage("내보내지 못했어요. 로그인 권한과 저장 증거를 확인한 뒤 다시 시도하세요.");
    } finally {
      if (request.current === controller) {
        request.current = null;
        setBusy(false);
      }
    }
  }

  return (
    <div style={{ textAlign: "right", maxWidth: 420 }}>
      <label>
        내보내기 공개 범위{" "}
        <select
          aria-label="내보내기 공개 범위"
          value={audience}
          disabled={busy || !enabled}
          onChange={(e) => setAudience(e.target.value)}
        >
          <option value="shared">공유용 마스킹 사본 (기본)</option>
          <option value="internal">관리자 내부 상세 사본</option>
        </select>
      </label>
      <button className="btn" type="button" disabled={busy || !enabled || !validTask} onClick={() => void download()}>
        {busy ? "보고서 생성 중…" : "종합 HTML 다운로드"}
      </button>
      {busy && (
        <button
          className="btn"
          type="button"
          onClick={() => {
            request.current?.abort();
            request.current = null;
            setBusy(false);
            setMessage("");
          }}
        >
          취소
        </button>
      )}
      <p style={{ fontSize: 12, color: "var(--muted)" }}>
        선택한 점검의 모든 저장 회차가 포함됩니다. 화면 필터와 집계는 적용되지 않습니다.
        {audience === "internal"
          ? " 내부 상세는 인증된 관리자용이며 외부 전달 전 민감정보 검토가 필요합니다."
          : " 공유 사본은 자유문장도 마스킹되어 재현 정보가 제한될 수 있습니다."}{" "}
        보호된 증거 참조에는 별도 접근 권한이 필요합니다.
      </p>
      {message && <p role="status">{message}</p>}
    </div>
  );
}
