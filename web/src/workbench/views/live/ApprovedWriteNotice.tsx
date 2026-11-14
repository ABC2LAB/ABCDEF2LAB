"use client";

import type { URLDiagnosisScope } from "@/lib/diagnosis/diagnosis-url";

import s from "../../workbench.module.css";

/** 서버가 반환한 실제 범위만 고지한다. 입력·모드 선택으로 쓰기 권한을 만들지 않는다. */
export function ApprovedWriteNotice({ scope }: { scope: URLDiagnosisScope | undefined }) {
  if (!scope?.origin || !scope.path_prefixes.length || !scope.methods.length) return null;
  if (scope.approval_mode !== "preauthorized_scope" && scope.approval_mode !== "workbench_selective_v1") return null;
  const writes = scope.methods.filter((m) => ["POST", "PUT", "PATCH"].includes(m));
  if (!writes.length) return null;
  return (
    <section className={s.isec} aria-label="쓰기 승인 안내">
      <h2>승인된 쓰기 범위</h2>
      <p>
        대상 <code>{scope.origin}</code>
      </p>
      <p>
        허용 경로 <code>{scope.path_prefixes.join(", ")}</code>
      </p>
      <p>
        허용 메서드 <code>{scope.methods.join(" / ")}</code>
      </p>
      <p className={s.note} role="note">
        어떤 쓰기 요청이 나갈지 미리 모두 알 수 없으며, 대상 데이터나 상태가 바뀔 수 있습니다. 복구 가능한 시험환경과
        진단 승인 범위를 확인하세요.
      </p>
      <p>
        {scope.approval_mode === "preauthorized_scope"
          ? `자동 모드: 사전 승인된 쓰기 메서드(${writes.join(" / ")})는 요청마다 수동 확인하지 않고 보냅니다.`
          : "사용자 승인 모드: 쓰기 요청은 요청마다 사람이 내용을 확인하고 승인한 뒤 보냅니다."}
      </p>
    </section>
  );
}
