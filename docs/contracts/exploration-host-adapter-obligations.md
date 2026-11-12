# 탐색 host adapter의 조립 의무

이 문서는 A 세션 API가 신뢰하는 host의 의무와 아직 검증하지 않는 것을 구분한다. 기존 탐색 계약의 팀 합의 상태를 바꾸거나 운영 경계의 수용 완료를 뜻하지 않는다. 현재 제품의 운영 adapter 기본값은 nil이다.

## Worker와 회차

host는 `Binding.TaskID`로 서버의 authoritative task resolver를 호출하고 `Server.ExplorationWorkerFor(task)`에서 받은 동일 Worker를 Open·Run 양쪽에 사용해야 한다. 다른 task의 provider·모델 예산을 공유 Binding과 짝짓지 않는다. A는 실행 도중 Worker pointer 교체를 거부하지만 임의 `llm.Provider` 내부의 task 참조를 반사해 검사하지는 않는다. 세션 API에 Worker와 Binding을 별도 전달했다는 사실이 그 결속의 증명이 아니다. 제품 pipeline의 신뢰한 서버 조립이 결속을 확인하고 기존 모델 정책·비용 원장을 유지한다.

## 브라우저 lease의 증명

`ExplorationBrowserInfo.PackageVersion`은 신뢰한 host의 attestation이며 MCP initialize의 `serverInfo.version`과 대조한 결과가 아니다. host는 보호된 이미지/패키지와 실제 실행 인수를 확인해야 한다. MCP 핀은 `@playwright/mcp@0.0.84`, 브라우저는 그 패키지로 설치한 Chromium이다. 실제 이미지 digest·패키지 검사·브라우저 실행은 별도 C 수용 근거로 확인하고, 문자열 자기 보고만으로 운영 수용을 주장하지 않는다.

host는 모델이 바꿀 수 없는 `--headless --isolated --no-webmcp --browser chromium`, 경계의 브라우저 입구 proxy, 격리된 출력 위치를 사용한다. `--caps`, 임의 init script, TLS 무시, 인증 헤더·쿠키 인수, 외부 CDP 연결은 주지 않는다. 같은 이름을 가진 WebMCP 도구는 이름 허용 목록만으로 식별할 수 없으므로 `--no-webmcp`와 실제 실행 설정의 검증이 필요하다. HTTP 제어 주소·이름·포트도 host가 고정한다.

host는 `file://`와 다른 로컬 자원 열기를 브라우저/도구 broker에서 차단하고, 제어 비밀·호스트 파일·다른 세션 프로필을 mount하지 않아야 한다. A의 TargetURL 검사나 네트워크 proxy가 모든 브라우저 파일 접근을 차단한다는 뜻은 아니다. 이 조건이 확인되지 않으면 lease를 공급하지 않는다.

## 직접 도구와 API Recon

검증된 직접 sandbox adapter가 아직 없으므로 현재 A의 public Worker profile과 Open은 non-nil `DirectBash`/`APIRecon`을 모두 실행/I/O 전에 거부한다. 이름이 Bash/Skill인 CoreTool도 예외가 아니다. 프로그램 소유 정적 Fetch와 MCP의 metadata 경로는 유지한다. 미래에 typed adapter를 검증한 뒤 이 제한을 별도 변경으로 열어야 한다. host는 일반 ToolAugment/전체 Skill registry/호스트 Bash를 넣지 않는다. 직접 도구는 회차에 결속된 별도 sandbox에서 직접 입구만 사용하며 브라우저/MCP 제어망·CDP·소켓·프로필에 닿지 않아야 한다. 이 배치는 실제 양성/음성 대조로 검증하기 전에는 켜지 않는다.

새 경로의 API Recon은 검토된 metadata-only 자산만 제한해서 공급한다. 원본 `harvest_static.py`, `spider_mpa.py`, `runtime_harvest.js`나 전체 저장소를 mount·실행하는 경로는 복원 완료로 보지 않는다. SKILL 문구만 바꿔서는 실행 제한이 생기지 않는다. A는 현재 generic CoreTool 주입을 거부하며 내부 파일 시스템/실행 제한의 구현으로 대신하지 않는다. 미래 adapter가 이 제한을 실제로 집행하지 못하면 Bash/Skill capability를 열지 않는다. 현재 운영 adapter가 없는 상태는 이 조건의 대체 증명이 아니다.

## 종료와 오류

모델 오류는 `model_error`, 실행 옵션 거부·기타 실행 불가는 `runtime_unavailable`, 턴/시간 상한은 `cap_exhausted`로 마감한다. 실패나 실행 전 거부를 completed로 기록하지 않는다. 취소·철회는 별도 사유를 유지한다.

정리는 모델·도구 차단 뒤 browser_close, private Finalize, MCP DELETE, browser lease, Boundary Close 순서다. 각 host callback은 cleanup context의 deadline을 지키고 자식 프로세스·프로필·연결의 실제 소유 자원을 정리해야 한다. 단계 하나가 실패해도 뒤 정리를 시도한다. 오류는 원문 대신 고정 `Failure.Stage/Kind`와 안전한 서버 로그로 남긴다. 에러 문자열·명령·URL·세션 참조·헤더·본문·자격 값은 이 진단에 포함하지 않는다. 이 정보는 문제 단계 식별용이며 E② receipt·감사·E③ 증거를 만들지 않는다.
