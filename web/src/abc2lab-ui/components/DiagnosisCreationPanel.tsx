import { useEffect, useState } from "react";

import { ApiError } from "../lib/api";
import { readURLCatalog, type URLDiagnosisCatalog, urlDiagnosisError } from "../lib/diagnosis-url";
import { NewDiagnosisPanel } from "./NewDiagnosisPanel";
import { URLDiagnosisPanel } from "./URLDiagnosisPanel";

type Capability =
  | { kind: "loading" }
  | { kind: "v2"; catalog: URLDiagnosisCatalog }
  | { kind: "v1" }
  | { kind: "unavailable"; message: string };

/** 읽기 전용 catalog로 버전을 선택한다. 변경 요청 실패 뒤 다른 버전으로 재실행하지 않는다. */
export function DiagnosisCreationPanel() {
  const [capability, setCapability] = useState<Capability>({ kind: "loading" });
  useEffect(() => {
    let active = true;
    void readURLCatalog().then(
      (catalog) => {
        if (active) setCapability({ kind: "v2", catalog });
      },
      (cause: unknown) => {
        if (!active) return;
        if (cause instanceof ApiError && [404, 501].includes(cause.status)) setCapability({ kind: "v1" });
        else setCapability({ kind: "unavailable", message: urlDiagnosisError(cause) });
      },
    );
    return () => {
      active = false;
    };
  }, []);
  if (capability.kind === "loading") return <p role="status">진단 관리 기능을 확인하는 중이에요.</p>;
  if (capability.kind === "unavailable") return <p role="alert">{capability.message}</p>;
  if (capability.kind === "v1") return <NewDiagnosisPanel />;
  return <URLDiagnosisPanel key={capability.catalog.ownerContext} catalog={capability.catalog} />;
}
