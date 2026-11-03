# 공유예산 공개 API의 실제 UID 수용시험

상태: **격리된 Docker box의 Linux 커널 경계 수용시험**.
[로컬 발급 계약](approval-budget-local-issuer.md)의 기존 same-UID 비공개 fixture를
확대 해석하지 않고, 공개 `budgetlocal.Listen/Submit`만 검증한다.
기준 dev: `88415fff88f1c66c4307ba370c360e6ad338efcc`.
열린 PR 코드의 병합에 의존하지 않으며 제품 코드·CLI·서버 기동·DB를 변경하지 않는다.

## 시험 경계

신규 `budgetlocal/real_uid_acceptance_linux_test.go`는 external test package이며
`linux && budgetuidacceptance` build tag가 필요하다.
기본 `go test`에는 포함되지 않고 Docker를 자동 실행하지 않는다.
태그만 켜서 호스트에서 실행하면 box guard가 실패하며 이를 skip/완료로 처리하지 않는다.

캐시된 Linux amd64 이미지를 먼저 inspect하고 다운로드 없이 정적 시험 binary만
읽기 전용으로 마운트한다. 이미지의 PostgreSQL entrypoint나 DB는 실행하지 않는다.
root coordinator가 소유한 container의 tmpfs 안에서만 디렉터리를 만들고 chown하며,
실제 server/operator/model 자식을 kernel credential API로 생성한다.
새 호스트 사용자·그룹이나 계정 설정을 만들지 않는다.

- Docker: `--network none`, read-only root, `--cap-drop ALL`.
- 추가 capability는 box 안 credential 생성·fixture 소유권 변경용
  `CHOWN/SETGID/SETUID` 세 개뿐이다.
- `no-new-privileges`, CPU/memory/PID/FD 상한과 writable tmpfs만 사용한다.
  캐시 PostgreSQL 이미지의 기본 data volume도 tmpfs로 덮어쓴다.
- 각 자식은 실제 UID/GID, 보조 group 0개를 보고하고 nonroot effective capability 0을
  단언한다. 신원 숫자를 인자로 받는 순수 판별 시험만으로 성공을 표시하지 않는다.
- server는 실제 별도 프로세스로 공개 Listen을 호출한다. client는 credential 강하·exec
  뒤 새 연결로 공개 Submit을 호출하며, 별도 SO_PEERCRED 관찰로 실제 server UID도 남긴다.
- 정상 종료는 coordinator의 private stdin pipe EOF → Server.Close다.
  launcher는 정확한 container ID·이름·run label을 다시 확인한 뒤 소유 container만 정리한다.

box guard는 명시 marker·Docker marker·PID1 coordinator·read-only root·root-owned tmpfs·
no-new-privileges·최소 capability를 확인한다. 이 guard는 실수로 호스트 root에서
helper를 실행하지 않게 하는 장치이며 악의적인 root에 대한 인증 증명이 아니다.

## 사례와 합격 조건

| 사례 | 실제 실행·합격 조건 |
|---|---|
| operator | server/operator/model의 서로 다른 kernel UID, callback 1회, peer UID 일치, 합성 응답 1개 |
| model / wrong operator | 각각 다른 실제 UID client, callback 0회·응답 metadata 0개 |
| server peer pin | listen 뒤 box 안 parent/socket owner만 잘못된 server pin에 맞춘다. filesystem 검사는 통과하지만 실제 server UID는 그대로이므로 Submit은 ErrUnknown·callback 0회 |
| root operator | operator UID 0·nonroot server/model, 실제 callback peer UID 0·합성 응답 1개 |
| root server | 실제 server UID 0에서 공개 Listen은 ErrInvalid·callback 0회 |
| UID 충돌 | server/operator, server/model, operator/model 충돌 각각 공개 Listen 거부 |
| client/server UID 충돌 | 실제 같은 UID client가 공개 Submit을 호출하면 ErrInvalid·callback 0회 |

peer-pin 사례의 owner 변경은 root coordinator가 자기 box tmpfs 안에서만 수행하며
Server.Close 전에 원래 server owner로 복구한다.
callback은 검증된 peer UID와 `IssuerRef` 일치를 확인하여 합성 fixture metadata만 반환한다.
BudgetAuthority·실제 승인 DB·Ledger·대상 HTTP·LLM은 사용하지 않는다.

비운영자 응답은 기존 AF_UNIX ECONNRESET 제한으로 ErrDenied 또는 ErrUnknown일 수 있다.
합격 기준은 callback/metadata 0이며 CLI의 거부 분류 문제가 해결됐다고 주장하지 않는다.

## 수동 실행

아래는 Linux·Go·Docker가 이미 준비되고 `postgres:16-alpine`이 **캐시된 경우**의 예다.
이미지/권한/credential API가 없으면 중단하며 pull·도구 설치·privileged 우회를 하지 않는다.
Docker daemon·host root·socket을 container에 마운트하지 않는다.

```sh
uid_build_dir="$(mktemp -d)"
CGO_ENABLED=0 GOPROXY=off GOTOOLCHAIN=local go test \
  -tags budgetuidacceptance -c -o "$uid_build_dir/smoke.test" ./budgetlocal
uid_image_id="$(docker image inspect postgres:16-alpine --format '{{.Id}}')"
uid_run_id="$(cat /proc/sys/kernel/random/uuid)"
uid_name="budget-uid-smoke-$uid_run_id"

uid_cid="$(docker create --pull=never --name "$uid_name" --user 0:0 \
  --network none --read-only --cap-drop ALL \
  --cap-add CHOWN --cap-add SETGID --cap-add SETUID \
  --security-opt no-new-privileges=true --pids-limit 96 \
  --memory 256m --memory-swap 256m --cpus 1 --ulimit nofile=128:128 \
  --tmpfs /work:rw,nosuid,nodev,noexec,mode=0755,size=16m \
  --tmpfs /var/lib/postgresql/data:rw,nosuid,nodev,noexec,mode=0700,size=1m \
  --mount "type=bind,src=$uid_build_dir/smoke.test,dst=/smoke.test,readonly" \
  --env ARTEX_BUDGET_UID_ACCEPTANCE=boxed-kernel-uids-v1 --env GOMAXPROCS=2 \
  --label com.abc2lab.task=budget-real-uid-acceptance \
  --label "com.abc2lab.run=$uid_run_id" \
  --workdir / --entrypoint /smoke.test "$uid_image_id" \
  -test.run='^TestBudgetRealUIDAcceptance$' -test.v -test.timeout=120s)"
```

create 뒤 inspect에서 ID·name·run label, privileged=false, network=none,
read-only root와 위 cap/tmpfs·binary의 read-only bind를 확인한다.
예상 밖 volume/mount가 있으면 시작을 보류하고 신원을 확인한 자기 container만 정리한다.

```sh
docker inspect "$uid_cid"
docker start --attach "$uid_cid"
docker inspect --format '{{.State.ExitCode}}' "$uid_cid"
```

성공은 coordinator와 10 case가 모두 pass, fail/skip 0, 실제 container exit 0일 때다.
생성/연결 결과가 불명확하면 같은 이름으로 create를 재시도하기 전에 exact ID/name/label을 확인한다.

```sh
if test "$(docker inspect --format '{{.Id}} {{.Name}} {{index .Config.Labels "com.abc2lab.task"}} {{index .Config.Labels "com.abc2lab.run"}}' "$uid_cid")" \
  = "$uid_cid /$uid_name budget-real-uid-acceptance $uid_run_id"; then
  docker rm --volumes "$uid_cid" &&
  rm -- "$uid_build_dir/smoke.test" &&
  rmdir -- "$uid_build_dir"
else
  printf '%s\n' 'container identity unverified; cleanup pending' >&2
fi
```

timeout 뒤에도 실행 중이면 동일 ID/name/label을 검증한 그 container만 stop한다.
다른 세션의 container/process는 보존한다.

## 이번 실측

2026-11-03, Go 1.26.3 linux/amd64 정적 binary와 캐시 이미지
`sha256:721873c34ceb9f8d8fc265984940dc982404c105f19ad51be9fdc5970a6080ea`에서 실행했다.
coordinator와 10 case가 각각 한 번씩 실행되어 **11 pass / 0 fail / 0 skip**,
실제 container exit 0을 확인했다. ID/name/task·run labels 재검증 후 소유 container를
제거하고 성공한 daemon 목록 조회에서 부재를 확인했다.

| 실제 UID·관찰 | callback / 합성 응답 |
|---|---|
| server 41001, operator client 41002, 관찰 server 41001, callback peer 41002 | 1 / 1 |
| model client 41003 또는 wrong operator 41004, 관찰 server 41001 | 각각 0 / 0 |
| filesystem owner·pin 41005, 실제 관찰 server 41001, operator client 41002 | 0 / 0 |
| server 41001, root operator client·callback peer 0 | 1 / 1 |
| root server 또는 세 가지 UID pair 충돌 | 각각 Listen 거부·0 / 0 |
| 실제 client/server 모두 41001 | Submit 거부·0 / 0 |

tag 포함 vet·전체 build와 기존 public config 단일 race 시험이 통과했다.
기본 tag의 파일 제외와 명시 tag의 포함도 `go list`로 확인했다.
static box 시험에는 race instrumentation을 사용하지 않았으며 위 11건과
별도의 기존 config race 1건을 구분한다.

## 해석과 남은 검증

이 시험의 UID는 단일 box 안의 같은 Linux user/PID namespace에서 관찰한 값이다.
host 계정·namespace mapping·실제 launcher/executor 격리는 검증하지 않는다.
연결 시 SO_PEERCRED는 연결 뒤 상속·위임·전달된 FD의 현재 RPC 발신자를 증명하지 않는다.
실제 host 인증·현재 instance/epoch·FD 비상속/비전달 경계는 별도 검토가 필요하다.

공개 Listen에 modelUID 숫자를 전달하는 것과 실제 모델의 모든 실행 경로·자손의
UID/권한·수명을 신뢰 host가 증명하는 것은 구분한다.
[신뢰 기동기 제안](approval-budget-identity-launcher-proposal.md)의 IL01/IL10에 관련된
boxed peer/guard 부분만 검증하며 다른 IL 수용시험을 완료로 표시하지 않는다.

기존 CLI의 private config/요청 파일 읽기, 실제 task/policy/reset issuer policy,
DB commit/ACK, budget 실행 결속, 서버 socket 활성화는 이번 시험 범위 밖이다.
결과는 실제 kernel 관찰과 합성 issuer callback을 구분하고 실제 승인·진단·운영 배포의
성공으로 계산하지 않는다. 공유 출력은 case의 UID 관찰·callback/응답 건수로 제한한다.
