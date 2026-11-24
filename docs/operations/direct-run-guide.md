# 제품 직접 실행 안내 — 화면으로 VAmPI·허가받은 URL 점검

운영자가 제품 서버를 직접 띄우고, Workbench 화면으로 연습 대상 VAmPI나 허가받은 URL을 점검하는 순서다. 설치 입력의 칸 하나하나와 보안 경계는 [제품 설치 문서](../../deploy/product/README.md)가 정본이다. 이 문서는 그중 실제로 누르는 순서만 모았다. 공식 비교 평가는 [평가 실행 절차](../evaluation/official-run.md)를 따르고, 이 순서로 돌린 회차는 평가 결과로 세지 않는다.

> **머지 뒤 갱신** 표시는 지금 dev에 아직 없는 기능이 들어갈 자리다. 들어오면 이 문서를 같이 고친다.

## 0. 시작 전에

- **대상**: 소유자가 진단을 허가한 URL만 쓴다(절대규칙 1). 대상 DNS에 TXT 레코드를 넣을 수 있어야 하고, IP 주소로 된 대상은 지원하지 않는다. 연습은 [VAmPI](../evaluation/practice-targets.md#vampi)로 한다.
- **모델**:
  - 로컬 모델 설치면 대상 데이터가 밖으로 나가지 않는다.
  - 외부 API(OpenRouter) 설치는 대상 응답·도구 출력이 제공자로 나가고 비용이 든다. 실제 고객 대상이면 시작 전에 고객사 승인을 받는다([외부 LLM 안내](../external-llm-notice-ko.md)).
- **같은 PC에서 다른 회차가 돌 때**: 측정 구간 규칙([평가 실행 절차](../evaluation/official-run.md))을 지킨다. 측정 중에는 Docker를 쓰지 않는다.
- **비밀 값**: 관리자 비밀번호·API 키·시험 계정 비밀번호·DNS 제어 토큰은 화면 입력과 보호 파일에만 둔다. 문서·채팅·커밋·로그 공유에 옮기지 않는다(절대규칙 2).

## 1. 보호 입력 준비

저장소 밖의 0700 디렉터리에 0600 파일로 둔다. 형식과 칸 설명은 [제품 설치 문서 '최초 설치'](../../deploy/product/README.md#최초-설치)에 있다.

| 파일 | 내용 | 가리키는 환경 변수 |
|---|---|---|
| 설치 입력 `install.json`(형식 `abc2lab.product-install/v2`) | 관리자 비밀번호(`admin_password`), 대상 보안 상한(`target_policy`), 설치 수명·실행 횟수, 모델(`local_model` 또는 `openrouter_manifest`) | `PRODUCT_INSTALL_FILE` |
| PostgreSQL 비밀번호 파일 | 임의 값 | `PRODUCT_POSTGRES_PASSWORD_FILE` |
| 탐색 경계 입력(탐색을 켤 때만): 경계 설정·CA 인증서·CA 개인키·경로 가림 키 | [탐색 경계·브라우저 샌드박스](../../deploy/product/README.md#탐색-경계브라우저-샌드박스설치-조직이-켬) | `EXPLORE_BOUNDARY_CONFIG_FILE`·`EXPLORE_CA_CERT_FILE`·`EXPLORE_CA_KEY_FILE`·`EXPLORE_PATH_KEY_FILE` |

설치 입력에서 직접 실행에 닿는 칸:
- `target_policy.max_requests`: 회차당 요청 상한이다. 대상 등록 화면이 이 값으로 등록한다.
- `target_policy.workflow`: 탐색·선택 흐름. 쓰기 검사를 하려면 `allow_reversible_writes: true`로 둔다.
- `target_policy.exploration`: 브라우저 탐색.
- `max_runs`·`valid_for_hours`: 리허설까지 포함해 넉넉히 잡는다. 결과 불명 회차도 횟수에 든다.

공통 환경 변수:
```bash
export PRODUCT_SOURCE_SHA="$(git rev-parse HEAD)"     # 빌드할 소스 전체 SHA
export PRODUCT_INSTALL_FILE=<보호 디렉터리>/install.json
export PRODUCT_POSTGRES_PASSWORD_FILE=<보호 디렉터리>/postgres-password
export PRODUCT_TAG=<이미지 태그>                       # 다른 사람 이미지와 겹치지 않게
export COMPOSE_PROJECT_NAME=<내 프로젝트 이름>          # 내리기·정리를 이 이름으로만 한다
export PRODUCT_PORT=<화면 포트>                        # 기본 8787
```

## 2. 제품 띄우기

**(가) 허가받은 실제 URL**: 제품 compose만 쓴다.
```bash
./install.sh                                    # 구성 검사 → 빌드 → up -d --wait
# 탐색을 켠 설치는 프로필을 같이 준다(install.sh는 프로필을 넘기지 않는다)
./scripts/product-compose.sh --profile exploration build
./scripts/product-compose.sh --profile exploration up -d --wait
```
설치 입력의 `target_policy`는 공개 IP·HTTPS 443이 기본이다. 사내망 대상이면 [제품 설치 문서](../../deploy/product/README.md#최초-설치)대로 좁은 `private_cidrs`·포트를 따로 검토해 넣는다.

**(나) VAmPI 연습**: 소유 시험 overlay를 겹친다. 시험 DNS·시험 CA·VAmPI를 같이 띄운다.
```bash
dc() { docker compose -f deploy/product/compose.yaml -f deploy/product/compose.acceptance.yaml \
  -f deploy/product/compose.exploration-acceptance.yaml -f deploy/product/compose.practice-vampi.yaml \
  --profile exploration "$@"; }
dc build api explore-browser fixture-init vampi   # 같은 이미지를 쓰는 서비스를 한꺼번에 빌드하면 'already exists'로 실패할 수 있어 이미지마다 하나씩
dc up -d --no-build
```
- 설치 입력에는 [소유 로컬 Docker 수용](../../deploy/product/README.md#소유-로컬-docker-수용)의 시험 값을 넣는다: `private_cidrs: ["172.31.253.0/24"]`, `allowed_ports: [9443]`, `resolver_address: "172.31.252.40:5353"`, `upstream_ca_file: "/target-tls/ca.pem"`.
- 이 묶음은 `scripts/product-compose.sh`를 거치지 않아 보호 파일 검사(저장소 밖·0600)를 스스로 확인한다.
- 내부 시험 망 주소가 호스트 망과 겹치면 overlay 주소와 설치 입력을 함께 바꾼다.

**준비 확인**: `curl -sf http://127.0.0.1:$PRODUCT_PORT/api/product/health`가 200이면 된다. 첫 빌드는 몇 분 걸린다.

## 3. 화면 주소

- `http://127.0.0.1:${PRODUCT_PORT:-8787}/`
- WSL에서 띄웠으면 Windows 브라우저에서 `http://localhost:<PRODUCT_PORT>`
- 호스트 공개는 loopback뿐이다. 원격 접근은 운영자의 HTTPS 역방향 프록시로만 한다.
- 화면 주소 뒤 해시로 바로 갈 수 있다.

| 해시 | 화면 |
|---|---|
| `#dashboard` | 대시보드 |
| `#new` | 새 점검 |
| `#running` | 진행 중인 점검 |
| `#finished` | 끝난 점검 |
| `#live` | 점검 진행 |
| `#report` | 보고서 |
| `#classify` | 계정 분류 |
| `#declaration` | 정책 선언 |
| `#e2flow` | 전송 통제 흐름 |
| `#controlpolicy` | 전송 통제 정책 |
| `#accessmap` | 접근통제 지도 |
| `#coverage` | 커버리지 |
| `#explore` | 탐색 결과 |

## 4. 관리자 로그인

- 사용자명은 `ABC2LAB`이다.
- 비밀번호는 설치 입력(`PRODUCT_INSTALL_FILE`이 가리키는 보호 파일)의 `admin_password` 값이다. 이 문서에는 적지 않는다.
- 잊었으면 같은 환경 변수로 `./reset-password.sh`를 실행해 숨김 입력으로 새로 정한다(12~72바이트).
- 로그인 실패 제한은 접속 주소별이다. 틀린 값으로 되풀이하지 않는다.

## 5. 모델

- **로컬 모델**: 설치된 프로필이 하나면 새 점검 2쪽 '분석 모델'에서 '로컬 실행'으로 기본 선택된다.
- **외부 API**: 새 점검 2쪽 '분석 모델' → '외부 API' → '제공자' → '외부 모델 API 키'에 키를 넣는다.
  - 키는 다시 화면에 나오지 않고, 다음 점검은 저장된 키를 다시 쓴다. 바꿀 때는 '키 바꾸기'를 누른다.
  - 키와 점검 범위만으로는 실행되지 않는다. 시작 때 외부 전송·비용 동의를 따로 받는다.
- **외부 모델 점검 횟수**(머지 뒤 갱신): 지금은 외부 API 설치 하나에 점검 1회이고, 점검마다 새 사용량 승인(manifest 승인 ID·만료·USD 상한)이 필요하다. 제한을 없애는 변경이 들어오면 설정의 점검당 비용 상한만 남는다([개발 계획](../development-plan.md)).
- **제공자 모드**: 설치 입력 `openrouter_manifest` v4의 `provider_routing`으로 정한다([OpenRouter 연결](../openrouter-first-diagnosis.md)).
  - 고정: `{"mode":"pinned"}`, 승인 제공자 하나만 쓴다.
  - 자동 넘김: `{"mode":"fallback","providers":[…]}`.
  - 제품 기본값은 자동 넘김이다: PrimeIntellect(`primeintellect/fp8`) → DeepInfra(`deepinfra/fp8`) 순서(둘 다 ZDR·고정 샘플링·도구 호출 지원, 2026-11-23 넘김 확인 뒤 기본값으로 올림). 평가 manifest는 늘 고정 모드다.
  - 호출마다 실제로 응답한 제공자는 사용량 기록(`returned_providers`)에 남는다.

## 6. VAmPI 띄우기·리셋 (VAmPI 연습만)

- (나) 묶음이 VAmPI를 띄운다. 대상 주소는 `https://vampi.test:9443/`이다(시험 DNS·시험 CA, 앞단 `target-front`).
- 원본 기본값을 그대로 쓴다: `VAMPI_VULNERABLE=1`(BOLA 있음), `VAMPI_TOKEN_TTL=60`(토큰 60초). 바꾼 회차는 회차 조건에 적는다.
- **시험 계정**: VAmPI 기본 계정은 비밀번호가 5바이트라 제품이 로그인하지 않는다. 제품은 6바이트 미만 비밀번호를 보호 값으로 등록하지 않는다.
  - 비관리자 계정 둘을 새로 가입시킨다(긴 무작위 비밀번호). 가입 본문에 `admin` 칸을 넣지 않는다.
  - 가입은 VAmPI 컨테이너 안에서 `POST http://127.0.0.1:5000/users/v1/register`(JSON `username`·`password`·`email`)로 보낸다(`dc exec vampi python …`). 비밀번호는 보호 파일에만 둔다.
  - 확인은 같은 방식으로 `POST /users/v1/login`이 200인지 본다.
- **리셋**(다음 점검 전, 특히 확정 단계를 돌린 뒤): 다음 셋을 하고, 화면에서 리셋 확인(9장)을 누른다.
  1. `dc up -d --force-recreate --no-deps vampi`: 앱 DB를 초기화한다.
  2. 컨테이너 안에서 `GET /createdb`.
  3. 같은 두 계정을 같은 값으로 다시 가입하고 로그인을 확인한다.

## 7. 대상 등록·소유 확인

`#new` 1쪽 '대상' → '점검할 사이트 주소'를 넣는다. 승인되지 않은 대상이면 '소유 확인 필요'와 **대상 등록하기**가 보인다.

1. '대상 등록하기'를 누르고 아래 칸을 채운 뒤 **대상 등록**을 누른다. 화면에 TXT '이름'(`_abc2lab-challenge.<호스트>`)과 '값'이 나온다.
   - **점검할 경로 (조회만)**: 한 줄에 하나씩 쓴다. `/api/`는 그 아래 전체와 query까지 허용하는 접두 승인이다. `=/`는 그 경로 하나만 허용하는 정확 일치다. 비우면 주소의 경로가 들어간다.
   - **점검 근거**: 20자 이상. 운영자가 직접 쓰는 확인이다.
   - **쓰기 동의 체크**(설치가 되돌릴 수 있는 쓰기를 허용할 때만 보임): 체크하면 GET·HEAD·POST·PUT·PATCH, 안 하면 GET·HEAD다. DELETE 요청은 보내지 않는다. 초기화·결제처럼 상태를 크게 바꾸는 경로는 점검 시작 때 제외 경로(8장)에 넣어야 막힌다.
   - **승인 유효 시간**: 4시간(기본)~30일. 시연 날까지 남게 고른다.
   - 회차당 요청 상한은 설치값이 자동으로 들어간다.
2. TXT를 대상 DNS에 게시한다.
   - 실제 URL: 대상 도메인의 DNS 관리 화면에 TXT 레코드를 추가한다.
   - VAmPI: 소유 시험 DNS 제어로 게시한다. loopback `PRODUCT_DNS_CONTROL_PORT`의 `/control`에 `{"name": <이름>, "values": [<값>]}`를 POST한다. 헤더 `X-Fixture-Control`의 토큰은 `fixture-dns` 컨테이너의 `/target-tls/control-token`에서 읽고 출력하지 않는다.
3. **TXT 레코드 확인**을 누른다.
   - 서버가 TXT 소유, DNS 주소, 설치 상한, 포트, TLS 인증서·공개키를 확인한다(대상 HTTP 진단은 하지 않는다).
   - 통과하면 '승인된 대상'과 경로·메서드·요청 상한·만료가 보인다.
4. 그만 쓸 대상은 **승인 철회**를 누른다. 새 실행이 막힌다.

## 8. 시험 계정·동의·정책 선언 (로그인 점검·확정 단계를 쓸 때)

**계정 분류**(`#classify`): '대상 선택'에서 승인된 대상을 고른다.
1. **대상 로그인 방식**의 칸을 채우고 **로그인 방식 저장**을 누른다. 칸: '로그인 경로 (POST)'·'본문 형식'·'사용자명 칸 이름'·'비밀번호 칸 이름'·'세션 위치'·'응답 JSON 칸 (점으로 구분)'·'요청 헤더 이름'·'헤더 값 앞붙임 (선택)'.
   - VAmPI 예: `/users/v1/login`, JSON, `username`·`password`, 응답 칸 `auth_token`, 헤더 `Authorization`, 앞붙임 `Bearer `.
2. '계정 라벨'을 넣고 **계정 등록** → **시험 표시** → 서버 진술을 읽고 **동의하고 표시**.
3. **자격 입력** → 사용자명·비밀번호 → **자격 등록**.
   - 보내면 칸이 바로 비워지고, 목록에는 '자격 등록됨'만 보인다.
   - 사용자명 앞뒤에 공백이 있으면 저장할 수 없다. 이전에 공백이 붙은 사용자명을 저장했다면 앞뒤 공백을 제거한 사용자명으로 자격을 다시 등록한다.
   - 비밀번호가 6바이트보다 짧으면 실행 때 그 계정으로 로그인하지 않는다.
   - 표시가 없거나 만료된 계정은 실계정으로 보고 모델에 넣지 않는다.

**외부 API 모델의 시험 계정 동의**: 새 점검 화면에 시험 계정 외부 전송 동의 체크가 나온다. 체크하지 않으면 시험 계정 없이 점검한다.

**정책 선언**(`#declaration`, 확정 단계용): '대상 선택' → '선언 입력'을 채우고 **선언 저장**을 누른다. '현재 선언'에 활성으로 보이면 된다.
- 선언 입력의 칸: '신원 확인'·'자원 종류'(공유 또는 소유자 전용)·'시험 객체 생성'·'객체 조회'·'확정 단계 시험 계정'.
- 확정 단계는 두 시험 계정이 인증되고 대상 등록에 쓰기 동의가 있을 때만 돈다.
- VAmPI 예:
  - 신원 경로 `/me`, 주체 ID `data.username`
  - 종류 `book`(소유자 전용, 소유자 전용 칸 `secret`)
  - 생성 POST `/books/v1`, JSON 본문 틀 `{}`, 표식 칸 `secret`, ID 칸 `book_title`
  - 조회 `/books/v1/{id}`, 데이터 경로 `.`
  - 소유자 계정 A, 교차 계정 B

## 9. 제외 경로·점검 시작

1. `#new` 2쪽 '점검 설정'에서 아래를 고른다.
   - **진단 모드**:
     - '사용자 승인': 탐색 뒤 점검할 경로를 고르고, 쓰기 메서드 요청은 한 건씩 승인한다.
     - 'AI 자동': 승인 범위 안에서 이어 가고, 승인한 쓰기는 건별 확인 없이 보낸다.
   - **제외 경로 · 선택**:
     - 한 줄에 하나씩 쓴다. 경로 단위라 그 경로와 아래 경로의 모든 메서드가 빠진다.
     - 와일드카드는 조각 하나 전체를 뜻하는 `*`만 된다. `?`가 든 값은 받지 않는다.
     - GET으로 상태가 바뀌는 주소(로그아웃·삭제 링크·구독 해지·이메일 확인 링크 등)를 넣는다. GET 요청은 따로 막지 않는다.
     - 대상 등록 화면에는 이 칸이 없다.
     - VAmPI 예: `/createdb`(DB 초기화), `/users/v1/*/password`(시험 계정 비밀번호 변경).
   - **시작 확인 · 필수**: 두 문장에 체크한다. 체크한 답은 점검 기록에 남는다.
     - "GET으로 상태가 바뀌는 주소를 확인해 제외 경로에 넣었거나, 그런 주소가 없음을 확인했습니다."
     - "점검 대상이 복사본(시험 환경)이거나, 원본이면 백업이 있습니다."
2. **승인 범위 확인** → 오른쪽 '점검 요약'에서 '안전 장치'를 읽는다.
   - 승인된 사이트·경로, http 대상 경고, 제외 경로 수, 요청·비용 상한과 승인 만료, 외부 모델 전송, 확정 단계 여부가 나온다.
3. 동의 체크 → **점검 시작** → **점검 보기**.
4. '대상 리셋 확인 필요'가 보이면 지난 확정 단계가 시험 객체를 남긴 것이다. 대상을 실제로 리셋하고(6장), 진술에 동의한 뒤 **리셋 확인**을 누른다. 그래야 다음 확정 점검이 시작된다.

## 10. 점검 중 볼 곳

| 볼 것 | 어디서 | 확인할 점 |
|---|---|---|
| 진행 화면 | `#live`(레일의 진행 중인 점검 → '이 점검 열기', 또는 `#running`) | 상태·모델 사용량, 오른쪽 진행 요약(전송 통제·탐색 현황·커버리지·점검 정보) |
| 전송 통제 | 진행 요약 '전송 통제' 칸, `#e2flow` '전송 통제 요약'·'호출 · 시도', `#controlpolicy` | 요청마다 허용·차단이 남는다. 제외 경로 요청은 거부로 남는다 |
| 후보 | `#live` '발견 후보' | 줄마다 '수동 검토 필요'다. 후보는 확정이 아니다 |
| 사용자 승인 | `#live` '확인 필요', 대시보드 결정 대기 | 경로를 골라 **선택한 범위로 점검 시작**. 쓰기 요청은 **승인하고 보내기** 또는 거부. 승인 만료 시각이 보인다 |
| 판정 | `#report`의 확정 단계 판정 표, 후보 상세(`#finding`), `#accessmap` | 판정 사유는 한국어 문구로 보인다. 증거가 모자라면 판단불가로 남는다(확정으로 보이지 않음) |
| 보고서 | 끝난 뒤 진행 화면의 '보고서 보기' 또는 `#report`, `#finished` | 저장된 결과로 만든 보고서. '내려받기' |

## 11. 멈추기

- 진행 화면의 빨간 **종료** → 확인 상자에서 다시 **종료**를 누른다. 되돌릴 수 없다.
  - 새 요청을 보내지 않는다.
  - 응답을 못 받은 요청은 전송 불명, 끝내지 못한 항목은 미실행으로 남는다.
  - 그때까지의 결과로 보고서를 만든다.
- **새 점검이 '진행 중인 회차'(busy)로 막힐 때**(진행 중인 점검이 없는데도):
  - 지금: 이전 회차의 자원 마감을 확인하지 못한 경우다. 제품을 `down` → `up -d --wait`로 재기동하면 풀린다([제품 설치 문서 '실행 경계와 영속 상태'](../../deploy/product/README.md#실행-경계와-영속-상태)). 결과 불명 기록·예약·실행 횟수는 그대로 남는다.
  - 머지 뒤 갱신 — 멈춘 작업 해제(C04): 관리자에게 **멈춘 작업 해제** 버튼과 사유 입력이 생긴다.
    - 막는 작업이 결과 불명이거나 중지 중이고, 활동이 기본 1200초 동안 없을 때만 풀 수 있다.
    - 풀면 준비 서비스가 스스로 재시작하므로 화면이 잠깐 끊긴다.
    - 누가·언제·사유·근거 값이 감사 기록으로 남는다.

## 12. 내리기 (내 프로젝트만)

```bash
# (가)
./scripts/product-compose.sh down                 # 데이터 볼륨은 남긴다(다시 올리면 기존 설치를 재사용)
# (나) VAmPI 연습: 볼륨은 남긴다(-v 금지 — 이 dc는 compose.yaml도 읽어 postgres-data·product-state·e2-host-state까지 지운다)
dc down --timeout 60
# 남은 것 확인: 내 프로젝트 라벨만
docker ps -a --filter label=com.docker.compose.project=$COMPOSE_PROJECT_NAME
```
- 정리는 내가 띄운 compose 프로젝트·라벨로만 한다. `docker rm -f $(docker ps -aq)` 같은 전체 삭제는 하지 않는다(같은 PC의 다른 회차를 지운다).
- 제품 설치에서 `down -v`는 영속 상태(키·승인·감사·보고서)를 지운다. 복구 절차로 쓰지 않는다.
- VAmPI 대상만 처음 상태로 되돌리려면 볼륨을 지우지 말고 리셋 절차(6장)를 쓴다. 연습 overlay에는 VAmPI 전용 볼륨이 없다.

## 13. 로그 위치

| 로그 | 보는 법 |
|---|---|
| 서비스 로그(준비 서비스·API·화면·E② Host) | `./scripts/product-compose.sh logs -f preparation api workbench e2-host`. (나)는 `dc logs -f …`. 오래 볼 것은 저장소 밖 파일로 받아 둔다 |
| 진단 운영 기록(JSONL) | 준비 컨테이너 `/p/data/diagnostic-logs/`(`product-state` 볼륨). 형식은 [진단 운영 기록](../first-diagnosis-diagnostic-logs.md) |
| E② Host 감사 저널 | `e2-host-state` 볼륨의 `journals/` |
| 탐색 경계 | `dc logs explore-boundary`(제품 설치는 경계 원자료를 내보내지 않는다) |

- 로그·저널·보고서는 결과물이다. 커밋하지 않는다(절대규칙 2).
- 쿠키·토큰·비밀번호 원문이 섞였을 수 있으니 공유할 때는 이름만 남기고 값을 가린다.

## 머지 뒤 갱신할 자리

- 5장: 외부 모델 설치의 점검 1회 제한·점검마다 사용량 승인 제거
- 11장: 멈춘 작업 해제(C04) 버튼·사유·기본 대기 시간·재시작 중 끊김
