// Package e3contract는 제품 E③ 확정 경로(#596)의 세 갈래(판정·E② 연동·화면)가 만나는 공통 계약이다.
//
// 이 패키지는 타입·인터페이스·검증 함수와 확정 단계 순서(Run)를 고정한다. 실제 E² 연동은 Transport·Journal 구현이,
// 정책 선언 저장·동결은 DeclarationReader·ResetGate 구현이 맡는다. 가짜 구현은 e3fake에 있고, 계약 시험은
// 가짜 구현으로 미구현·검증 실패·결속 불일치가 CONFIRMED로 이어지지 않음을 고정한다.
//
// 지키는 것:
//   - 판정 값은 oracle.Evaluate만 만든다. 이 패키지는 사실(CaseInput)을 모을 뿐 판정 값을 정하지 않는다.
//   - 결속·진위가 깨지면 판정 행을 만들지 않고 회차 상태를 confirmation_integrity_failed로 남긴다.
//   - 증거가 모자라면 판정 행은 UNDECIDABLE이고 제품 사유 코드를 따로 남긴다(오라클 사유 목록은 그대로).
//   - 제품 확정은 product_declaration 정책 출처만 받는다.
//   - 표식 값은 Marker 타입 밖으로 직렬화되지 않고, 확정 단계를 시작할 때 E²의 그 회차 보호 값으로 등록된다.
//
// 평가 경로(transmit·evidencebind·e3record의 등록 GET)는 이 패키지를 쓰지 않는다.
package e3contract
