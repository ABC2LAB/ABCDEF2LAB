# 원본 ARTEX 페이지 수집기 — 조회 범위·인증 계약

이 수집기는 고정 원본 ARTEX의 **무필터 전역 findings 페이지 조회**만 수행한다.
지원 원본은 `jiwoochris/artex-global`의 `fdfb7a17b7da5e2de716c0e98475ec77a41c64c6`다.
source SHA는 호출자의 선언이며 실제 서버의 소스나 실행 출처를 인증하지 않는다. 기존 출처·AGPL-3.0 표시는 유지한다.

근거는 고정 SHA의 [findings handler](https://github.com/jiwoochris/artex-global/blob/fdfb7a17b7da5e2de716c0e98475ec77a41c64c6/server/server.go#L2023)와
[JWT middleware](https://github.com/jiwoochris/artex-global/blob/fdfb7a17b7da5e2de716c0e98475ec77a41c64c6/server/auth.go#L445)다.
구현·테스트보다 먼저 이 경계를 고정한다. 합성 loopback 서버로 검증하며 실제 계정·서버·모델·스캔은 사용하지 않는다.

## 조회 범위

- 호출자가 등록한 HTTP loopback origin만 사용한다. `localhost`는 127.0.0.1로 연결하고 숫자 loopback IPv4/IPv6도 허용한다.
- 고정 GET `/api/exploration/findings?page=N&limit=M`만 생성한다. 시작은 page=1, limit는 호출자가 고정한 1..200이다.
- origin에 사용자 정보·경로·query·fragment를 넣을 수 없다. 임의 path·query·필터·task 설정 API는 제공하지 않는다.
- `task` 조회는 페이지 모드와 다르게 bare array를 반환하므로 혼용하지 않는다. q/status/vulnclass 등 필터도 전부 제외한다.
- 응답은 `{items,total,page,page_size}` 페이지 객체여야 한다. page/page_size가 요청과 정확히 같고 total은 수집 내내 같아야 한다.
- total=0도 page=1의 빈 응답 한 장을 수집한다. 누락/중복·페이지별 길이·source/binding 참조는 기존 병합기로 검증한다.
- page 수·응답 크기·시간 제한과 검토된 분류 매핑을 먼저 검증하고 실패하면 송신하지 않는다.
  첫 페이지가 선언한 page 수가 상한을 넘으면 다음 페이지를 요청하지 않는다. 실패 후 재시도·재시작은 자동으로 하지 않는다.

전역 조회는 한 task/run의 기록이 아니다. 평가 러너는 전용 baseline 인스턴스/자료 범위와 실행 중지·자료 안정성을 별도로 보장해야 한다.
수집기는 task/run/instance를 추정하거나 붙여 주지 않는다. 응답에 snapshot/version/cursor가 없으므로 수집 중 같은 total을 유지한 자료 교체까지 검출할 수는 없다.
최종 성공은 [페이지 병합기](baseline-pages.md)의 입력 선언 일관성 검증이며, 실제 서버 전체 수집 완료·근거 실존·귀속을 증명하지 않는다.

## 인증과 데이터

- 인증은 호출자가 메모리 keyword 인자로 제공하는 Bearer 토큰만 사용한다. config/result에 토큰을 포함하지 않는다.
- 토큰은 1..8192자의 ASCII Bearer 문자열이며 공백·제어문자·헤더 삽입을 거부한다. 토큰을 해석하거나 새로 발급하지 않는다.
- 로그인·토큰 갱신·인증 재시도·환경변수/명령행/URL token·cookie 인증을 제공하지 않는다. 별도 CLI도 만들지 않는다.
- Authorization은 등록한 loopback origin으로 보내는 GET에만 포함한다. redirect·환경 proxy·cookie jar·자동 retry를 사용하지 않는다.
- 응답의 Set-Cookie는 사용하지 않는다. 원본 body/헤더/토큰은 파일·로그·오류·최종 결과에 포함하지 않는다.
- 원본 페이지는 함수 내부 메모리에서 기존 추출기/병합기로만 전달한다. 반환은 `BaselinePageMerge`의 비민감 분류·상태·참조다.
  원본 자유 문자열을 정화하여 재공유하는 DLP 기능은 아니며 분류 매핑은 평가자가 검토해야 한다.
- HTTP 상태·통신·JSON·페이지 검증 실패는 입력값을 포함하지 않는 고정 코드의 예외로 반환하고 부분 결과는 내지 않는다.

## 구현 범위

신규 수집기·테스트·이 문서만 추가한다. 기존 스냅샷 수집기의 origin/HTTP framing·EOF 검증을 재사용하고
기존 `extract_baseline`과 `merge_baseline_pages`에 연결한다. HTTP reader나 페이지 병합 규칙을 새로 복제하지 않는다.
기존 회차 작성기와 다른 세션의 DLP·E②·서버·귀속 계약 파일은 수정하지 않는다.
서명/키·지문·판정·GT 상관·A5 baseline 채점 연결·실측은 이 기능에서 생성하지 않는다.

## 사용

```python
from baseline_pages_collector import BaselinePagesEndpoint, collect_baseline_pages

endpoint = BaselinePagesEndpoint(
    origin=registered_origin,
    page_size=fixed_page_size,
    timeout_seconds=socket_idle_timeout,
    max_page_bytes=page_wire_limit,
    max_total_bytes=total_body_limit,
    max_pages=page_count_limit,
)
result = collect_baseline_pages(
    endpoint,
    reviewed_vocabulary,
    source_sha=declared_source_sha,
    bearer_token=memory_only_bearer,
)
```

모든 설정값은 호출자가 명시한다. `page_size`는 1..200, `max_pages`는 양의 정수,
`timeout_seconds`는 양의 유한수이며 bool은 정수로 취급하지 않는다.
`max_page_bytes`·`max_total_bytes`는 각각 양의 정수이고 8 MiB 이하여야 한다.
분류 매핑은 기존 추출기와 같은 형식의 64 KiB 이하 검토된 비민감 JSON 데이터다.
호출자의 매핑을 복사해 첫 요청 전에 검증하고 모든 페이지에서 같은 사본을 사용한다.

`max_page_bytes`는 HTTP chunk framing을 포함한 응답 wire 상한이며, `max_total_bytes`는 디코딩 body 길이의 누적 상한이다.
각 응답을 읽을 때 잔여 누적 상한도 wire 읽기에 적용하므로, chunked 응답은 body 합계가 맞아도 framing 때문에 보수적으로 거부될 수 있다.
응답 body의 실제 누적 길이도 별도로 확인한다. HTTP 헤더는 이 body/wire 계산에 포함되지 않는다.
`timeout_seconds`는 연결/읽기의 socket 비활성 제한이며 전체 수집 시간의 deadline이 아니다.

반환 `BaselinePageMerge`는 선언 SHA·total·page_size·수집 page_count와 비민감 findings만 포함한다.
모든 페이지를 받은 뒤 기존 병합기로 source/binding 교차 중복을 검사하며 실패하면 부분 결과는 반환하지 않는다.
총 페이지·페이지 echo·total·각 페이지의 길이 오류는 다음 GET 전에 중단한다.
null/미수집 근거와 명시적 빈 배열·서로 다른 source 행의 finding/asset 공유 의미는 그대로 유지한다.

기존 `snapshot_collector._endpoint`·`_read_response`와 `baseline_extract`의 strict JSON helper를 재사용한다.
내부 helper 결합은 이 모듈의 호환성 경계이며 기존 모듈을 변경할 때 실제 합성 HTTP 통합 테스트로 다시 확인한다.
`http.client`의 debuglevel은 요청마다 0으로 고정해 프로세스 전역 debug 설정도 Authorization을 출력하지 못하게 한다.

## 오류와 검증

API는 `BaselineCollectionError`의 고정 코드로 실패를 알린다. 원래 예외의 값·헤더·응답 body·토큰은 재출력하지 않는다.
잘못된 소스/설정/인증/분류 매핑은 요청0회, HTTP 오류·timeout·redirect는 재시도0회를 요구한다.
인증 오류를 로그인/토큰 갱신/쿠키 경로로 우회하지 않는다. 합성 테스트의 토큰도 stdout이나 파일에 기록하지 않는다.

```bash
cd scripts/eval
python3 -I -B -m unittest discover -s tests -p test_baseline_pages_collector.py -q
python3 -I -B -m unittest discover -s tests -q
```

소유 loopback 서버로 페이지/헤더/인증 경계, 자료 변동·누락·참조 충돌·크기 상한·strict JSON·framing/EOF·timeout,
redirect 목적지/proxy 수신0건, 쿠키 미전달·debug/예외 원문 미노출과 기존 병합기 연결을 시험한다.
소켓 제한 sandbox에서는 이 합성 서버 테스트에 실행 권한이 필요하며 skip을 성공으로 기록하지 않는다.
