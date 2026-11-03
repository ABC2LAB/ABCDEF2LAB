# 공유예산 실행 결속 DB 정본

2026-11-03 · 이동찬

이 구현은 [승인 DB 정본](./approval-budget-store.md)의 현재 epoch에 이미 게시된 승인 하나를 새 실행에 결속한다. 승인 발급·HTTP 전송·감사는 수행하지 않는다.

- 서버가 실제 task/case/policy/reset과 실행 만료를 검증한 뒤 `BindRun`에 전달한다. 메서드는 승인 Key/Binding과 전체 Scope가 같은지 확인하고, 새 `RunRef`를 발급한다. 모델은 RunRef와 BudgetRef를 발급하지 않는다.
- `budget_run_bindings`에 RunRef, 전체 Scope, BudgetRef, 승인 요청 참조, 발급자, epoch, 만료를 한 행으로 확정한다. RunRef는 전역 기본키이고 행 변경·삭제·truncate를 차단한다. 같은 RunRef로 새 전송기·Sequence를 재생성하지 않는다.
- 실행 만료는 승인 만료보다 늦을 수 없다. DB 커밋과 현재 epoch·잠금 재검사가 성공한 뒤에만 기존 Ledger 포인터를 반환한다. 결속은 예약이 아니며 Preview·설정 검증도 예산을 차감하지 않는다.
- 저장 결과가 불명확하면 이번 호출은 실행 입력을 반환하지 않는다. 미확인 RunRef로 전송을 시작하거나 같은 RunRef를 재발급하지 않는다. 재시작 뒤 구 epoch의 결속·승인은 자동 복원되지 않는다.

운영자 승인 발급 방식은 **운영자 전용 로컬 명령**으로 선택됐으며 이 PR에는 구현하지 않았다. 실제 서버 어댑터는 인증된 승인 발급, task/policy/reset·자원·계정 검증, 실행 gate, journal/checkpoint 생성과 `StopAndWait`를 결속해야 한다. 이 DB 코어만으로 SB01–SB12의 서버 통합이나 대상 송신 통제를 완료했다고 보지 않는다.
