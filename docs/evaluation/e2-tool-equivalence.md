# E② 샌드박스 도구 동등성 실측

[평가 프로토콜](evaluation-protocol.md) §2의 수용 조건("샌드박스 안에서 쓸 수 있는 도구 트리가 baseline 엔진 이미지와 같아야 같은 조건")을 실제 이미지로 잰 기록이다.
결과 원문과 측정 스크립트는 [근거 파일](profiles/evidence/e2-sandbox-tool-probe-20261110.json)에 있다.

## 방법

같은 셸 스크립트를 baseline 엔진 이미지 컨테이너와 `eval-e2` 샌드박스에서 각각 돌려 79개 항목을 비교했다.
항목은 도구 경로, 버전 출력, 파이썬·노드 동작, HTTP·TCP·DNS 도달, 경로 유무, 프록시·인증서 환경 변수 이름이다.

`eval-e2`는 Compose와 같은 옵션(`seccomp-e2.json`, read-only, `/tmp` tmpfs)으로 띄웠다. 이미지 빌드 시점 차이가 섞이지 않게 baseline 이미지 위에 빌드했다.
대상은 HTTP 임시 서버 하나다. HTTPS 대상과 실제 모델이 고르는 명령은 재지 않았다.

## 결과

| 시점 | 같음 | 다름 |
|---|---|---|
| 수정 전 | 42 | 37 |
| 수정 후 | 57 | 22 |

수정 전에는 샌드박스의 `python3`가 3.11.2(baseline 3.12.16)였고 `pip`이 없었다. `/etc/alternatives`가 없어 `awk`·`vim`·`nc`·`telnet`이 끊긴 링크였고 `nmap`은 라이브러리를 못 찾았다.
`/usr/local/bin`, `/usr/local/lib/python3*`, `libpython3*`, `/usr/sbin`, `/etc/alternatives`, `/etc/ld.so.cache`를 읽기 전용으로 넣고 PATH를 이미지의 해석 순서에 맞춰 이 차이를 없앴다.
`curl`, `wget`, 파이썬 `urllib`의 HTTP 응답은 수정 전후 모두 baseline과 같다.

## 남은 차이 (선언 제안)

프로토콜 §2에 따라 선언된 차이로 올리는 제안이다. 확정은 PM 몫이다. 같은 내용을 [선언 차이 목록](profiles/official-e2-declared-differences.json)에 두고, 러너가 그 해시를 회차 기록에 남긴다.

| 구분 | 항목 | 이유 |
|---|---|---|
| 프록시 밖 네트워크 | `dig`, `/dev/tcp`, `nc`, `nmap`, 노드 `fetch` | 샌드박스는 빈 netns이고 HTTP 프록시로만 나간다. 지원하지 않는 경로를 막는 E②의 동작이다. 노드 `fetch`는 프록시 환경 변수를 따르지 않는다 |
| 브라우저 도구 | `npm`, `npx`, `playwright`, `playwright-cli`, `/usr/lib/node_modules`, `/root/.cache/ms-playwright` | 넣지 않았다. 브라우저 트래픽은 지금 E②가 통제하는 경로가 아니다 |
| 이미지 자료 | `/usr/share/nmap`, `/usr/share/vim` | 넣지 않았다. `nmap`은 어차피 대상에 못 닿고 `vim`은 비대화형 실행에서 쓸 일이 없다 |
| 구성 파일 | `/etc/passwd`, `/etc/resolv.conf`, `/etc/ssl/certs` | 계정 파일은 가리고, DNS는 쓰지 않으며, 인증서는 `SSL_CERT_FILE`·`CURL_CA_BUNDLE`로 E② CA를 준다 |
| skills | `/app/skills` | E2 worker에 skills가 없다(프로토콜 §2의 구성 차이) |
| 환경 변수 | 프록시·CA 변수 추가, `PYTHON_VERSION`·`PYTHON_SHA256` 없음 | 샌드박스 환경은 helper가 고정한다 |
