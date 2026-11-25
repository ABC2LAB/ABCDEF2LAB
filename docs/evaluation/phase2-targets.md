# 2단계 대상 묶음 (초안)

2단계는 VAmPI 밖의 허가받은 다른 대상에서 제품 경로가 끝까지 도는지 보는 단계다. 대상마다 띄우는 법, 로그인 설정, 제외 경로, 정책 선언, 이 대상에서 확인할 수 있는 우리 기능을 한 묶음으로 적는다.

순서는 NodeGoat → crAPI → DVWA이고, 마지막에 Juice Shop으로 고장 찾기만 한다(사용자 결정 2026-11-26). 날짜는 사용자 일정을 따른다.

이 문서는 공식 저장소의 파일을 읽고 쓴 **초안**이다. 실제로 띄워 확인한 값이 아니다. 각 절의 '러너 확인' 항목은 러너가 대상을 처음 띄울 때 채운다. 띄우는 일은 러너가 무거운 작업 차례에 한다.

## 0. 공통

### 0.1 지킬 것

- 대상 이름·경로·필드 이름은 설정과 이 문서에만 둔다. 코드에 넣지 않는다(CLAUDE.md).
- 비밀번호는 이 문서에도 남기지 않는다. 시드 계정의 공개 기본 비밀번호도 마찬가지다. 시험 계정 자격은 저장소 밖 보호 파일(0600)에서 읽어 제품 API 요청 본문으로만 넣는다(CLAUDE.md 규칙 2, [VAmPI 절차](e3-vampi-acceptance.md) §4와 같음).
- 각 대상의 알려진 취약점(공식 README·튜토리얼)은 사람이 결과를 읽을 때만 본다. 엔진 입력(목표 문장·탐색·판정·선언)에 넣지 않는다([연습 대상](practice-targets.md)과 같은 규칙).
  - 아래 '확인할 수 있는 것'에 적은 취약 위치는 결과를 읽을 사람을 위한 것이다.
  - 제외 경로와 정책 선언은 운영자 입력이다. 대상 구조(어떤 GET이 상태를 바꾸는지, 어떤 자원을 누가 갖는지)만 담는다.
- 대상 이름은 제품 목표 문장으로 모델에 간다. 그래서 VAmPI 제품 회차처럼 대상마다 따로 이름(예: `nodegoat.test`)을 쓰고, Juice Shop 수용시험 이름은 쓰지 않는다.
- 대상 컨테이너는 회차 전용 내부 망에만 붙이고 바깥으로 나가지 못하게 한다. NodeGoat·crAPI에는 대상 서버가 입력받은 URL로 직접 요청하는 기능이 있다. 이 요청은 E②를 지나지 않는다(대상의 동작이다). 그래서 망 격리로 막는다.
- 이미지 버전 고정:
  - 태그가 아니라 digest로 고정한다(`docker image inspect --format '{{index .RepoDigests 0}}' <이미지>`). 소스 빌드 대상은 저장소 커밋 SHA와 만든 이미지 ID를 기록한다.
  - 고정 값은 회차 조건에 남긴다.
- 메모리:
  - 공식 문서에 수치가 없는 대상은 이 문서에 짐작으로 적지 않는다. 러너가 처음 띄울 때 `docker stats` 최대값을 '러너 확인' 칸에 적는다.
  - 무거운 작업 시작 조건(`heavy_ok.sh`)은 그대로다.
- 상태를 바꾸는 회차 전에는 대상을 초기화한다(CLAUDE.md 규칙 6). 대상마다 초기화 방법을 적는다.

### 0.2 제품 로그인 설정(`TargetLogin`)으로 되는 것과 안 되는 것

정본은 `diagnosisprep/product_contract.go`의 `ValidTargetLogin`이다.

- 되는 것:
  - `POST` 하나에 사용자명·비밀번호 두 칸을 싣는다. 본문은 `form`(urlencoded) 또는 `json`이다.
  - 세션은 둘 중 하나로 받는다.
    - 로그인 응답의 쿠키 이름 목록(`cookies`)
    - JSON 응답 칸(`json_field`, 점으로 이은 경로)의 값을 헤더(`header`)에 접두(`prefix`)와 함께 싣는다(VAmPI: `auth_token` → `Authorization: Bearer …`).
- 안 되는 것:
  - 로그인 페이지에서 CSRF 토큰을 먼저 받아 같이 보내는 방식
  - 두 칸 밖의 고정 칸(예: `Login=Login`)
  - 여러 단계 로그인(OTP·캡차)
  - 리다이렉트를 따라가야 세션이 생기는 방식
- **성공 판정의 한계**:
  - 인증 준비는 로그인 응답 상태가 400 미만이고, 지정한 쿠키나 JSON 칸이 응답에 있으면 '인증됨'으로 본다. 로그인 뒤 신원 확인 요청은 하지 않는다(`server/e2_http_auth.go` `authPrepOne`).
  - 그래서 로그인에 실패해도 200으로 다시 그리면서 새 세션 쿠키를 주는 앱이면, 비밀번호가 틀려도 '인증됨'으로 보인다.
  - 이런 대상은 회차 전에 운영자가 두 계정의 로그인을 따로 확인한다.
- 로그인 경로는 대상 승인 경로 접두 안이어야 한다.

### 0.3 정책 선언(E③ 증거 판정의 확정 단계)으로 되는 것과 안 되는 것

정본은 `e3contract/declaration.go`, 예시는 [VAmPI 선언](e3-vampi-acceptance.md) §5다.

- 확정 단계를 돌리려면 다음이 모두 필요하다.
  - 신원 확인 `GET`의 응답이 JSON이고, 그 안에 사용자 식별자 경로가 있다.
  - 소유자 전용(`owner_only`) 자원 종류가 있다. 그 종류를 `json`/`form` 본문으로 만들 수 있고, 본문에 표식을 넣을 소유자 전용 **텍스트** 칸이 있다.
  - 객체 ID를 생성 응답 JSON 경로(`id_path`)나 요청 본문 칸(`id_field`)으로 알 수 있다.
  - 조회가 `{id}` 하나만 있는 경로 틀이다. 쿼리(`?id=`)는 쓸 수 없다.
  - 조회 응답이 JSON이다.
- 조건을 못 채우는 대상은 선언을 저장하지 않는다. 그러면 확정 단계는 `declaration_missing`으로 꺼지고, 그것이 정상 결과다. 모델 단계의 진단·후보는 그대로 돈다.

## 1. NodeGoat

### 1.1 띄우는 법

- 공식 사전 빌드 이미지가 없다. OWASP/NodeGoat 저장소의 `docker-compose.yml`은 웹을 소스(`build: .`, `node:12-alpine` 두 단계)로 만든다.
- 고정 방법:
  - 저장소를 커밋 SHA 하나로 고정해 빌드한다. SHA와 만든 이미지 ID를 기록한다.
  - DB는 `mongo:4.4`를 digest로 고정한다.
  - 빌드 단계의 `node:12-alpine`도 digest로 고정한다.
- 서비스:
  - `web`: 포트 4000. 환경 변수 `MONGODB_URI=mongodb://mongo:27017/nodegoat`.
  - `mongo`: 27017, 내부만.
- **시작할 때마다 DB를 초기화한다.** `web`의 시작 명령이 `node artifacts/db-reset.js && npm start`다.
  - 초기화 = 웹 컨테이너 재시작이다.
  - 회차 중 가입한 계정·메모·기여금은 재시작 때 지워진다.
- 제품 회차 overlay는 VAmPI와 같은 방식으로 앞단이 `web:4000`을 가리키게 하고, 대상 이름은 `nodegoat.test`로 한다(러너 몫).
- 메모리: 공식 수치 없음 → 러너 확인.
- 러너 확인(2026-11-26, 회차 전용 내부 망·일회용 컨테이너):
  - [x] 커밋 `c5cb68a7084e4ae7dcc60e6a98768720a81841e8`(master 끝, 2023-06-21)
  - [x] 빌드 기반 `node@sha256:d4b15b3d48f42059a15bd659be60afe21762aae9d6cbea6f124440895c27db68`, DB `mongo@sha256:4be76f674fc4b27859816811b8baa3c51830eb1dbf4ca81a51e26b79edd662ef`(`--user mongodb`)
  - [x] 만든 이미지 `abc2lab-practice-nodegoat:c5cb68a7084e` = `sha256:e6bbbf4eb77959a5497d9afbce23e0e8d8dc112fe7623f9252757d0fe887d5b7`, 시작 명령은 원본 compose 그대로
  - [x] `docker stats` 최대: web 45.4MiB, mongo 63.1MiB. 로그인 확인 동안 2초 간격으로 잰 값이라 회차 부하는 아니다. 회차 때 다시 잰다.
  - [x] 첫 화면 `GET /` → 302 `/login`
  - [x] 웹 재시작 뒤에도 시드 `user1`·`user2` 로그인 302 `/dashboard`(재시작 초기화 동작)

### 1.2 로그인 설정

```json
{"method":"POST","path":"/login","format":"form","username_field":"userName","password_field":"password","cookies":["connect.sid"]}
```

- 폼 칸은 `userName`·`password` 두 개다.
  - CSRF 미들웨어(`csurf`)는 원본에서 주석 처리돼 꺼져 있다. 폼에 빈 `_csrf` 숨은 칸이 있지만 서버가 보지 않는다.
  - 그래서 지금 제품 로그인 설정으로 된다.
- 세션 쿠키는 express-session 기본 이름 `connect.sid`다(`key` 설정이 주석).
- 성공하면 302로 `/dashboard`(관리자는 `/benefits`)로 간다. 실패하면 200으로 로그인 화면을 다시 그린다.
- **주의(0.2 성공 판정의 한계)**:
  - 세션 설정이 `saveUninitialized: true`라, 로그인에 실패해도 새 `connect.sid`를 줄 수 있다.
  - 그러면 비밀번호가 틀린 계정도 제품에서 '인증됨'으로 보이고, 진행 화면 계정 경고(#941)도 뜨지 않는다.
  - 회차 전에 운영자가 두 계정으로 로그인해 302 `/dashboard`를 확인한다.
  - 제품 개선 후보: 쿠키 방식 로그인의 성공 판정에 리다이렉트 위치나 확인 요청을 더하는 것. 개발 계획 후보로 올린다.
- 러너 확인(2026-11-26): **오판이 실제로 생긴다.**

  | 시도 | 상태 | 위치 | `connect.sid` |
  |---|---|---|---|
  | `user1` 맞음 | 302 | `/dashboard` | 있음 |
  | `user2` 맞음 | 302 | `/dashboard` | 있음 |
  | `user1` 틀린 비밀번호 | 200 | 없음 | **있음** |
  | 없는 계정 | 200 | 없음 | **있음** |

  - 성공과 실패는 상태(302/200)와 위치(`/dashboard`)로만 갈린다. 지금 제품 인증 준비로는 두 경우가 다 '인증됨'이다.
  - 그래서 NodeGoat 회차는 회차 전에 운영자가 두 계정 로그인을 꼭 따로 확인한다.

### 1.3 시험 계정

- 시드 계정 `user1`·`user2`(둘 다 관리자 아님, 같은 권한 등급)를 시험 계정 A·B로 쓴다.
  - 웹 재시작마다 `db-reset.js`가 다시 만들므로 초기화 뒤에도 그대로 있다.
  - 비밀번호는 `db-reset.js`의 공개 기본값이다. 이 문서에는 적지 않고 보호 파일에 넣는다. 길이는 E② 보호 최소(6바이트) 이상이다.
- 관리자 시드 계정(`admin`)은 쓰지 않는다. 권한 등급이 달라 교차 비교가 흐려진다.
- 새로 가입해 쓰려면 `POST /signup`(form: `email`·`userName`·`firstName`·`lastName`·`password`·`verify`)을 쓴다. 다만 재시작마다 지워지므로 회차마다 다시 만들어야 한다.

### 1.4 제외 경로 초안

| 경로 | 까닭 | 처리 |
|---|---|---|
| `GET /logout` | 세션을 지운다(상태를 바꾸는 GET). 모델이 부르면 시험 계정 세션이 끊긴다 | 제외 |
| `GET /research` | 대상 서버가 쿼리로 받은 URL로 직접 요청한다(E② 밖의 대상 동작) | 대상 망을 격리한다. 격리를 확인하지 못하면 제외 |
| `GET /learn` | 쿼리로 받은 URL로 리다이렉트한다 | 제외하지 않는다. 다른 origin은 E②·탐색 경계가 막는다 |

- HTTP로 부를 수 있는 초기화 경로는 없다. 초기화는 재시작 때의 스크립트다.
- 상태를 바꾸는 POST(`/profile`·`/contributions`·`/memos`·`/benefits`·`/signup`)는 제외하지 않고 쓰기 동의(판 v2)와 승인 모드로 다룬다. 재시작으로 되돌릴 수 있으니 '리셋할 수 있는 쓰기'로 등록한다.
  - `POST /contributions`는 입력을 서버에서 코드로 실행한다. 모델 입력에 따라 대상 프로세스가 죽을 수 있다.
  - 대상이 죽으면 그 회차는 거기까지고, 다음 회차 전에 재시작(=초기화)한다.

### 1.5 정책 선언 초안

**선언을 저장하지 않는다**(확정 단계 `declaration_missing`). 까닭(0.3 조건):

- 모든 화면이 서버가 그리는 HTML이다. JSON API가 없다. 신원 확인 JSON도, 조회 JSON도 없다.
- 사용자가 만드는 소유자 전용 자원 중 표식을 넣을 텍스트 칸이 있는 것이 없다.

| 자원 | 누가 보나 | 만들 수 있나 | 메모 |
|---|---|---|---|
| allocations(`/allocations/:userId`) | 소유자 전용이어야 함 | 아니오(시드·가입 때 자동) | 경로의 사용자 ID로 조회 |
| memos(`/memos`) | 모든 로그인 사용자(공유) | 예(form) | 공유라 확정 대상 아님 |
| contributions(`/contributions`) | 소유자 | 예(숫자 칸만) | 텍스트 칸 없음 |
| profile(`/profile`) | 소유자 | 수정만 | 생성 아님 |
| benefits(`/benefits`) | 관리자용 목록 | 수정만 | 권한 등급 비교 대상 |

### 1.6 이 대상에서 확인할 수 있는 것

| 우리 기능 | 확인 |
|---|---|
| 인증 준비(form·쿠키) | 된다. 다만 1.2의 성공 판정 오판을 꼭 본다 |
| 세션 다시 받기(`refresh_session`)·세션 가림 | 된다 |
| 진행 화면 계정 경고(#941) | 틀린 비밀번호가 '인증됨'으로 보이면 경고가 안 뜬다. 오판 여부 확인 항목 |
| 탐색 | 서버가 그리는 링크·폼이라 SPA가 아니다. 크롤러·브라우저 탐색 모두 쓸 수 있다 |
| 제외 경로 집행(E② 거부) | `/logout` |
| 쓰기 동의·승인 모드 | POST 폼이 많다 |
| IDOR | 모델 단계 후보로만 나온다. E③ 확정은 위 까닭으로 안 된다. 선택 후보 검증(#950, 초안)은 지금 인증 세션이 필요한 대상의 새 실행을 거부해서 쓸 수 없다 |
| 비 IDOR 규칙 판정(#925) | 보안 헤더 미들웨어(helmet)가 꺼져 있어 헤더 없음·배너 유형의 관찰이 나올 수 있다 |
| e2-flow 총계·요청 목록·`closed_reason`·보고서 | 된다 |

## 2. crAPI

### 2.1 띄우는 법

- 공식 compose: OWASP/crAPI의 `deploy/docker/docker-compose.yml`과 `.env`. 공식 안내는 `--compatibility`로 띄운다(메모리 제한 적용).
- 이미지는 `crapi/<서비스>:${VERSION}`이고 `.env`의 기본값은 `latest`다.
  - 문서에 릴리스 태그 이름이 없다. 러너가 그때의 이미지를 받아 서비스마다 digest를 기록한다.
  - `postgres:14`·`mongo:4.4`·`chromadb/chroma:latest`도 digest로 고정한다.
- 서비스: identity·community·workshop·chatbot·web·postgresdb·mongodb·chromadb·mailhog·gateway-service, 모두 10개다.
- 진입:
  - 웹·API `http://<대상>:8888`(web)
  - 메일 확인 MailHog `:8025`
  - `.env`가 `TLS_ENABLED=true`라 8443도 열린다.
  - `LISTEN_IP`는 `127.0.0.1`이다.
- 메모리:
  - compose의 컨테이너 제한 합은 1394M(main 브랜치, 문서 수치가 아니라 제한값의 합)이다.
  - chatbot·chromadb는 제한이 없다.
  - 실제 사용은 러너 확인. 대상 넷 중 가장 무겁다.
- chatbot 서비스는 바깥 LLM을 부를 수 있다(설정 확인 필요). 망 격리 상태에서 실패해도 다른 서비스와 무관한지 러너가 본다.
- 초기화: compose를 볼륨까지 내리고(`down -v`) 다시 띄운다.
- 러너 확인:
  - [ ] 서비스별 digest
  - [ ] `docker stats` 최대값
  - [ ] 8888 200
  - [ ] chatbot 없이도 나머지가 뜨는지

### 2.2 로그인 설정

```json
{"method":"POST","path":"/identity/api/auth/login","format":"json","username_field":"email","password_field":"password","json_field":"token","header":"Authorization","prefix":"Bearer "}
```

- JSON 로그인 + Bearer 토큰이라 VAmPI와 같은 방식이다. 지금 제품으로 된다.
- 토큰 칸 `token`은 OpenAPI 스키마(`JwtResponse`)에서 읽은 것이다.
- 러너 확인:
  - [ ] 실제 로그인 응답의 토큰 칸 경로
  - [ ] 틀린 비밀번호의 상태 코드(400 이상이면 제품이 '로그인 거절'로 본다)
  - [ ] 토큰 수명

### 2.3 시험 계정

- 둘을 새로 가입한다: `POST /identity/api/auth/signup` JSON `name`·`email`·`number`·`password`.
- 확인할 것:
  - 가입 뒤 메일 확인이 있어야 로그인되는지
  - 비밀번호 규칙(스펙에 없음)
- 차량을 쓰는 기능은 가입 메일(MailHog)의 VIN·pincode로 `POST /identity/api/v2/vehicle/add_vehicle`을 해야 한다. 두 계정 모두 차량을 넣어 둔다(러너 확인).
- 관리자 계정(chatbot 설정에 있는 값)은 쓰지 않는다.
- 러너 확인:
  - [ ] 가입·로그인 절차
  - [ ] 차량 추가 절차
  - [ ] 두 계정이 같은 역할(`ROLE_USER`)인지

### 2.4 제외 경로 초안

| 경로 | 까닭 | 처리 |
|---|---|---|
| `POST /identity/api/v2/user/reset-password` | 시험 계정 비밀번호가 바뀐다 | 제외 |
| `POST /identity/api/v2/user/change-email` | 시험 계정 로그인 이름이 바뀐다 | 제외 |
| `POST /identity/api/v2/user/verify-email-token` | 위와 함께 쓰인다 | 제외 |
| `POST /identity/api/auth/forget-password` | 메일로 OTP를 보낸다 | 제외 |
| `POST /identity/api/auth/v2/check-otp`·`/v3/check-otp` | OTP로 비밀번호를 바꾼다 | 제외 |
| `GET /identity/api/v2/user/videos/convert_video` | GET이 서버에서 변환 작업을 돌린다 | 제외 |
| `GET /workshop/api/mechanic/receive_report` | GET이 정비 보고서를 만든다 | 제외 |
| `POST /workshop/api/merchant/contact_mechanic` | 대상 서버가 본문의 URL로 직접 요청한다 | 대상 망을 격리한다. 격리를 확인하지 못하면 제외 |
| `DELETE /identity/api/v2/user/videos/{id}`·`/admin/videos/{id}` | 지운다 | 쓰기 동의에서 DELETE를 켜지 않으면 막힌다. 켜면 제외 |
| chatbot 경로 | 바깥 LLM 호출 가능 | 러너가 경로를 확인한 뒤 제외 |

- 그 밖의 POST·PUT(글·주문·쿠폰·상품·반품)은 제외하지 않고 쓰기 동의와 승인 모드로 다룬다. 초기화(볼륨까지 다시)로 되돌릴 수 있다.

### 2.5 정책 선언 초안

- 신원 확인: `GET /identity/api/v2/user/dashboard`, 식별자 `email`(최상위 칸, 스펙 기준). `id`도 있다.
- 자원 종류 후보와 0.3 조건:

| 종류 | 생성 | 조회 | 공유 | 확정 가능? |
|---|---|---|---|---|
| community post | `POST /community/api/v2/community/posts` JSON `title`·`content`, ID `id`(응답 최상위) | `GET /community/api/v2/community/posts/{id}` | 공유(커뮤니티 공개) | 아니오(공유) |
| workshop order | `POST /workshop/api/shop/orders` JSON `product_id`·`quantity`, ID `id` | `GET /workshop/api/shop/orders/{id}`, 데이터 `orders` | 소유자 전용 | 아니오(표식을 넣을 텍스트 칸이 없음) |
| mechanic report | `POST /workshop/api/merchant/contact_mechanic`(텍스트 `problem_details`) | `GET /workshop/api/mechanic/mechanic_report?report_id=` | 소유자 전용 | 아니오(조회가 쿼리 ID, 생성 요청에 대상 서버 요청 URL 칸) |
| vehicle | JSON 생성 없음(가입 메일 VIN) | `GET /identity/api/v2/vehicle/{uuid}/location` | 소유자 전용 | 아니오(생성 불가) |

- **지금 선언 모양으로 확정할 수 있는 종류가 없다.** 처음에는 선언을 저장하지 않고 돌린다.
- 제품 확장 후보:
  - 조회 경로의 쿼리 ID
  - 숫자 칸만 있는 자원의 표식(예: 표식 값을 다른 공개 칸에 넣지 않고 소유자 전용 메모 칸을 쓰는 방식)
  - 둘 다 2단계 결과를 보고 정한다.
- 러너 확인:
  - [ ] 신원 응답 실제 모양
  - [ ] 주문 조회가 다른 계정에 403인지(정상 대조)

### 2.6 이 대상에서 확인할 수 있는 것

| 우리 기능 | 확인 |
|---|---|
| 인증 준비(json·Bearer)·세션 다시 받기·세션 가림 | 된다(VAmPI와 같은 방식) |
| 탐색 | 화면이 SPA(React)다. API 경로는 화면 동작을 따라가야 보인다. 브라우저 탐색이 필요하고 탐색 경계 집행을 본다 |
| 여러 서비스 경로(identity·community·workshop) | 한 origin 아래 접두가 다른 경로들. 승인 경로 접두를 `/`로 둘지 서비스별로 둘지 정한다 |
| 제외 경로 집행 | 2.4 |
| 쓰기 동의·승인 모드 | POST·PUT이 많다 |
| IDOR | 모델 단계 후보로 나온다. E③ 확정은 2.5 까닭으로 안 된다. 선택 후보 검증(#950, 초안)은 인증 세션이 필요한 대상이라 지금은 거부된다 |
| e2-flow 총계·요청 목록·`closed_reason`·보고서 | 된다. 엔드포인트가 많아 많은 엔드포인트 보고서 경로(#908·#921로 고친 경로)도 본다 |

## 3. DVWA

### 3.1 띄우는 법

- 공식 compose: digininja/DVWA의 `compose.yml`.
  - 웹: `ghcr.io/digininja/dvwa:latest`. 커밋마다 새로 빌드되고 버전 태그가 없다. 받은 이미지의 digest를 기록한다.
  - DB: `mariadb:10`, digest로 고정한다.
- 포트는 `127.0.0.1:4280` → 80이다.
- 환경 변수(`config.inc.php.dist`가 읽음):
  - `DEFAULT_SECURITY_LEVEL`: 기본 `impossible`. 연습에는 `low`로 둔다. 보안 등급 쿠키를 따로 다루지 않아도 된다.
  - `DISABLE_AUTHENTICATION`: 기본 `false`. 3.2를 본다.
- 첫 실행에 DB를 만들어야 로그인된다(`setup.php`의 '데이터베이스 만들기/초기화', POST + CSRF 토큰). 운영자가 브라우저로 한 번 한다.
- 초기화: 볼륨까지 내리고 다시 띄운 뒤 `setup.php`를 다시 한다.
- 메모리: 공식 수치 없음 → 러너 확인.
- 러너 확인:
  - [ ] 이미지·DB digest
  - [ ] `docker stats` 최대값
  - [ ] `setup.php` 뒤 첫 화면

### 3.2 로그인 설정 — **지금 제품으로 못 한다**

- 로그인 폼(`POST /login.php`) 칸은 `username`·`password`·`Login=Login`과 숨은 CSRF 토큰 `user_token`이다. 서버가 토큰을 검사한다(`checkToken`).
  - 토큰은 로그인 페이지 GET 때 세션에 묶여 생긴다.
  - 0.2의 '안 되는 것' 둘(토큰 먼저 받기, 두 칸 밖 고정 칸)에 해당한다.
- 세션 쿠키는 PHP 기본 이름 `PHPSESSID`다(파일에 명시는 없음).
- 선택지:
  - (가) **`DISABLE_AUTHENTICATION=true`로 익명 회차**: 로그인 없이 모든 모듈에 들어간다. 시험 계정·계정 비교는 없다.
  - (나) 제품에 '로그인 페이지에서 토큰 칸을 읽어 같이 보내는' 로그인 방식을 더한 뒤 계정 회차. 이것은 제품 기능 추가라 2단계 결과를 보고 정한다.
- 추천: 2단계에서는 (가)로 진단 경로만 본다. (나)는 로그인 방식 확장 후보로 개발 계획에 올린다.
- 시드 사용자(공개 기본값): `admin`(관리자)·`gordonb`·`1337`·`pablo`·`smithy`. 비밀번호는 적지 않는다.

### 3.3 제외 경로 초안

| 경로 | 까닭 | 처리 |
|---|---|---|
| `GET /logout.php` | 세션을 지운다 | 제외 |
| `/setup.php`(GET·POST) | POST가 DB를 통째로 지우고 다시 만든다 | 제외 |
| `/security.php` | POST로 보안 등급을 바꾼다 | 제외 |
| `GET /vulnerabilities/csrf/` | low·medium에서 GET 쿼리로 관리자 비밀번호를 바꾼다(상태를 바꾸는 GET) | 제외 |

- 명령 실행·파일 올리기 모듈은 대상 컨테이너 안에서 명령이나 파일이 남는다. 제외하지 않고 쓰기 동의로 다룬다. 회차 뒤 초기화한다.

### 3.4 정책 선언 초안

**선언을 저장하지 않는다.** 화면이 HTML이고 신원 확인·조회 JSON이 없다. `api` 모듈은 Composer 의존성이 있어야 동작하고, 응답 형식은 확인하지 않았다(러너 확인).

### 3.5 이 대상에서 확인할 수 있는 것

| 우리 기능 | 확인 |
|---|---|
| 로그인 방식이 없는 대상 처리 | (가)면 계정 없는 회차. 계정을 등록했는데 로그인 방식을 못 두는 경우 화면의 '로그인 방식 없음' 안내(#914·#941) |
| 탐색 | 서버가 그리는 페이지·폼. 모듈마다 폼이 있다 |
| 비 IDOR 진단 후보 | 여러 유형의 모델 후보가 나온다. 규칙 판정(#925) 대상 유형이 있는지 본다 |
| 제외 경로 집행 | 3.3 |
| 쓰기 동의 | POST 폼 |
| IDOR·E③ | 안 된다(계정 없음, HTML) |

## 4. Juice Shop — 고장 찾기만

- 용도: 제품 경로가 이 앱에서 끝까지 도는지(로그인·세션 다시 받기·SPA 탐색·보고서 크기 등)만 본다.
  - **챌린지 결과(점수판·해결 수)는 공식 측정 전까지 보지 않는다**(CLAUDE.md 규칙 7). 이 문서에도 챌린지 내용은 넣지 않았다.
  - 개발 계획의 'Juice Shop은 공식 측정과 그 직전 파일럿에만' 줄은 이 결정으로 바뀐다(같은 PR에서 고침).
- 예외 조건([동결 기록](freeze-record.md) §6.4처럼 적는다): 개발 계획이 Juice Shop을 공식 측정에만 쓰게 한 까닭은 "미리 돌리면 결과를 보고 고치는 일이 생길 수 있어서"다. 고장 찾기는 찾은 고장을 바로 고치는 일이라 챌린지 결과를 보지 않아도 그 까닭에 일부 걸린다. 그래서 두 가지를 지킨다.
  1. **고칠 수 있는 범위**: 앱과 상관없는 제품 경로의 고장(로그인 처리, 세션 다시 받기, 탐색·보고서·화면 경로의 오류 등)만 고친다. 이 앱에 맞춘 설정·프롬프트·규칙 조정은 하지 않는다(CLAUDE.md '이 앱 하드코딩 금지'와 같은 선).
  2. **밝힐 것**: 고장 찾기에서 찾아 고친 것의 목록(PR 번호와 한 줄 설명)을 공식 측정 결과와 함께 밝힌다.
- 띄우는 법:
  - `bkimminich/juice-shop:v15.0.0`, 포트 3000.
  - 공식 측정 준비가 쓰는 이미지·digest를 그대로 쓴다(평가 프로토콜, [동결 기록](freeze-record.md)).
  - 시작할 때마다 DB를 시드로 되돌린다. 초기화 = 재시작이다.
- 로그인 설정:

  ```json
  {"method":"POST","path":"/rest/user/login","format":"json","username_field":"email","password_field":"password","json_field":"authentication.token","header":"Authorization","prefix":"Bearer "}
  ```

  - 실패는 401이라 제품이 '로그인 거절'로 본다.
  - 러너 확인:
    - [ ] Bearer 헤더만으로 로그인 뒤 요청이 되는지(앱이 `token` 쿠키도 읽음)
- 시험 계정:
  - `POST /api/Users` JSON `email`·`password`·`passwordRepeat`(보안 질문 칸은 러너 확인)로 둘을 만든다.
  - 재시작마다 지워진다.
- 제외 경로·정책 선언: 이 문서에서 새로 정하지 않는다.
  - 공식 측정 설정(평가 프로토콜의 제외 규칙, 선언 없음)을 그대로 쓴다.
  - 고장 찾기 설정이 측정 설정과 달라지면 찾은 고장이 측정 때와 다를 수 있어서다.

## 5. 바로 다음 할 일

1. 러너: NodeGoat 1.1 띄우기와 '러너 확인' 칸 채우기(1.2 오판 확인 포함), 제품 overlay.
2. 기존 세션: 러너 확인 결과로 이 문서 고치기. 1.2 성공 판정 오판이 실제면 제품 개선 후보를 개발 계획에 올리기.
3. crAPI·DVWA는 같은 순서로 한다.
