# 탐색 대상 철회와 경계 마감

2026-11-13 기준 dev `f536b3553404c0c2e3e06f938041766b9ae7728e`의 잔여 연결을 보강했다. 이미 병합된 탐색 프록시·Chromium/MCP 분리·직접 Bash·탐색/진단 합계 요청 예산을 다시 구현하지 않는다. 이 문서의 현재 상태는 **IMPLEMENTED**이며 전체 제품 Docker 수용 결과는 별도 회차에 기록한다.

## 실제 제품 경로

제품 대상 철회는 승인 저널을 먼저 영속화하고, 그 대상에 결속된 활성 탐색 lease를 제어 소켓으로 `close(target_ended)`한 뒤 실행 문맥을 취소한다. 다른 대상의 문맥은 취소하지 않는다. 원격 close를 기다리는 동안 전역 active 잠금을 잡지 않아 다른 대상의 attach를 불필요하게 막지 않는다. 자격 사본 제거와 기존 E② 권한 재확인은 유지한다.

호스트는 실제 lease를 session/target/revision 결속으로 등록한다. Open 전과 원격 Open 뒤에 같은 보호 승인 상태를 다시 검사해 철회와 Open의 경쟁을 닫는다. 주기적인 Status도 승인 변경을 확인한다. 재승인이나 새 revision이 옛 lease를 다시 활성화하지 않는다. 세션 private 마감 전에 보호된 TargetEnded 확인을 적용해 취소와 철회 경쟁이 `cancelled`로 섞이지 않게 한다. 모델 출력이나 공개 JSON이 이 callback을 만들지는 않는다.

## 실패와 요청 몫

한 번 마감 결과가 실패·활성·연결 잔류·결속 불일치이면 해당 회차의 요청 몫은 불명으로 고정한다. 뒤의 정리 재시도가 성공해도 옛 회차의 진단 예산 증명을 다시 만들지 않는다. 자원 정리는 계속 시도하며 유효한 최종 상태를 확인한 lease만 활성 목록에서 뺀다. 철회 저널이 저장된 뒤 close가 실패하면 `target_revocation_close_failed`를 반환하고 자식 실행 취소도 수행한다. 원문 소켓 오류나 자격 값은 공개 사유에 넣지 않는다.

Open 응답만 유실된 경우에는 실제로 열린 경계가 있을 수 있다. 제어 클라이언트는 소유 lease를 반환해 마감과 실제 요청 수를 확인한다. 명시적인 서버 거절과 전송·응답 불명은 구분한다. 기존 `ExplorationCharge`와 E+D≤N의 검증 조건은 완화하지 않는다.

경계 Manager는 inactive 표식만으로 이전 요청의 종료를 가정하지 않는다. ActiveConnections가 남으면 새 Open을 거부한다. 새 세션 게시 전에 이전 유휴 터널을 정리하고, 지연된 옛 watcher는 현재 세션이 같은 경우에만 터널을 닫는다. 승인 만료는 target_ended, 탐색 자체 시간 상한은 cap_exhausted로 구분한다.

## 검증 근거

소유 TLS 수신기·실제 탐색 프록시·Unix 제어 소켓·보호 대상 저널 fixture로 정상 승인 요청1 뒤 철회 반환 시 원격 inactive/target_ended/연결0, 이후 대상 도달 증가0을 확인했다. 이미 확보한 private 관측은 남고 다른 대상의 문맥은 취소되지 않았다. 송신과 철회 경쟁, 제어 소켓 장애, 복구 뒤 sticky unknown, Open 중 승인 변경, 재승인 뒤 옛 lease, private 마감 사유를 검증했다. 이는 실제 DNS 소유 증명 등록이나 실제 모델 수용을 대신하는 fixture가 아니다.

- 관련 5패키지 선택 race: 121 PASS / 0 FAIL / 0 SKIP.
- vet: exploreboundary·exploration·exploresandbox·server·제품 cmd 통과.
- Linux·Windows·Darwin 전체 Go 빌드 통과.
- 원본 파일을 변경하지 않는 Go overlay 변이 4개: 직접 close 제거, 불명 마감 해제, Open 뒤 재검사 제거, private 사유 가드 제거 모두 해당 assertion FAIL로 탐지.
- 옛 watcher 간섭·미마감 세션 교체의 독립 반례는 수정 전 2 FAIL, 수정 뒤 PASS.

원문 로그·대상 응답·자격 값·빌드 산출물은 저장소에 넣지 않는다. 실제 제품 Docker의 P/B/D 항목과 인증 S07~S09, 원본 보존율은 별도 실행 결과 없이 COMPLETE로 표시하지 않는다.
