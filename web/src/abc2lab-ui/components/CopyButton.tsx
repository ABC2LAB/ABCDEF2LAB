import { useState } from "react";

import { useTimeouts } from "../lib/hooks";

/** 클립보드 쓰기 결과를 표시하고 1.2초 후 초기화한다. */
export function CopyButton({ text }: { text: string }) {
  const [status, setStatus] = useState<"idle" | "copying" | "copied" | "failed">("idle");
  const timers = useTimeouts();
  async function copy() {
    timers.clearAll();
    setStatus("copying");
    try {
      await navigator.clipboard.writeText(text);
      setStatus("copied");
    } catch {
      setStatus("failed");
    }
    timers.set(() => setStatus("idle"), 1200);
  }
  let label = "복사";
  if (status === "copied") label = "복사됨";
  if (status === "failed") label = "복사 실패";
  return (
    <button
      className="vcopy"
      type="button"
      disabled={status === "copying"}
      onClick={() => {
        void copy();
      }}
    >
      {label}
    </button>
  );
}
