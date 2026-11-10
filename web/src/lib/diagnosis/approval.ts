import { createContext, useContext } from "react";

/**
 * 승인 화면이 다룰 승인 요청(intercept pending) id.
 * 대시보드의 "승인하기"가 고른 요청의 id 를 여기에 실어 승인 화면으로 넘긴다.
 * 비어 있으면 승인 화면은 대기 목록을 보여 주고 거기서 고르게 한다.
 */
export type ApprovalTarget = {
  pendingId: number | null;
  /** 승인 화면 안에서 다른 요청을 고르거나(id) 목록으로 돌아갈 때(null) */
  selectPending: (id: number | null) => void;
  /** 이 요청을 골라 승인 화면으로 이동 */
  openApproval: (id: number) => void;
};

export const ApprovalContext = createContext<ApprovalTarget>({
  pendingId: null,
  selectPending: () => undefined,
  openApproval: () => undefined,
});

export function useApprovalTarget(): ApprovalTarget {
  return useContext(ApprovalContext);
}
