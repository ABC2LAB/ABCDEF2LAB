# 탐색 로그인 출처 대조 (`jindo.exploration-login-evidence/v2`)

이 계약은 PR #974의 탐색 `exploration_unaudited` 한계를 좁힌다. E② 서명 저널을 대체하거나 E③ 취약점 판정 권한을 늘리지 않는다.

제품 탐색 경계가 닫힌 뒤, 엔진은 로그인 시도 행과 **별도로** 경계 요청 기록 전체를 페이지 끝까지 읽고 `omitted == 0`을 확인한다. `transport_ref`가 가리키는 경계 순번의 승인 대상 `forwarded` 기록과 사적 관찰을 읽어 회차·세션·작업·대상 판·원본 digest, HTTP 메서드, 정확한 경로, 응답 상태, 실제 전달 및 응답 수신 여부를 대조한다. 경계 기록의 `path_pattern`은 마스킹된 표시용이므로 정확한 경로 검증에는 쓰지 않는다. 중복 순번·누락된 순번·읽기 실패·차단 또는 외부 요청은 근거가 되지 않는다.

검사를 통과한 비민감 projection은 회차의 0700 상태 디렉터리 안 `exploration-login-evidence.json`으로 0600 원자 저장한다. `schema`, `run_ref`, `session_ref`, `job_id`, `target_ref`, `target_revision`, `source_digest`, `sends[]`를 담는다. 각 `send`에는 순번·메서드·정확한 경로·상태·경계 전달·승인 대상·관찰 전달·응답 수신 여부만 있다. 계정 아이디, 비밀번호, 쿠키, CSRF 값, 원문 응답은 담지 않는다. 쓰기 또는 조회 실패 시 기존 `login-attempts.unavailable` 표지를 지우지 않는다. 표지가 남으면 진단 단계 로그인 시도와 E③ 재사용은 차단된다.

E③는 새 파일이 있으면 보호 파일 검사 후 선택한 탐색 성공 행과 같은 순번·회차·작업·대상 판·메서드·경로·상태를 대조한다. 일치하면 내부 출처 상태 `exploration_observed`를 기록한다. 파일이 손상됐거나 행이 일치하지 않으면 `learned_login_transport_unverified`로 막고 이전 성공이나 로그인 설정으로 넘어가지 않는다. 파일이 없는 **기존 회차**는 현재 정책인 `exploration_unaudited`로 남긴다. 새 출처 상태는 인증한 계정의 독립 확인이나 E② 감사 완료를 뜻하지 않는다. 경계 관찰에는 응답 본문 완료와 계정 결속이 없으므로 이를 검증했다고 주장하지 않는다.

신규 제품 회차의 승인 해시에 결속된 실행 정책은 `exploration_login_evidence: required/v2`와 `exploration_source_digest`를 기록한다. v2 projection은 기존 필드에 `task_id`를 추가한다. E③는 근거 파일의 존재, v2 스키마, 실행 권위의 작업 ID, 원본 digest를 독립적으로 대조한다. 유실·손상·불일치 시 `learned_login_transport_unverified`로 차단하고 이전 성공, 저장된 방법 또는 기존 로그인 설정으로 넘어가지 않는다. 탐색이 실행되지 않은 회차도 빈 v2 projection을 인계한다. 동일 파일의 재시도만 허용하며 다른 내용으로 덮어쓰지 않는다. 중복·별칭 JSON 키도 거부한다.

정책 필드가 없는 기존 회차는 명시적으로 `exploration_unaudited` 호환 정책을 유지한다. 기존 v1 projection은 읽을 수 있지만 신규 정책의 근거로 쓸 수 없다. 기존 회차까지 새 출처 근거를 필수로 할지, 탐색 관찰에 계정·응답 완료를 더할지는 팀 결정이 필요하다. 그 결정 전에는 과거 회차를 일괄 무효화하지 않는다.
