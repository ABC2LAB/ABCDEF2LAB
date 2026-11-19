# 일반 E② v3 현재 head 저장 코어 — 구현 범위

`e2checkpoint`는 `transmit/auditlog` 전용 `auditcheckpoint`와 별도 스키마를 사용한다. 코어의 `Migrate`는 구조만 만들며 epoch, 독립 실행 권한, PostgreSQL 역할 또는 감사 키를 발급하지 않는다. `Store`는 호출자가 등록한 동일 epoch·실행·게시자·저널·키 참조를 인증하는 Host lease를 필요로 한다. 독립 Host와 제품 실행 연결은 아직 없다.

신규 Docker 제품 설치는 이 스키마를 만들고 `product_e2_head_writer` **NOLOGIN** 역할에만 head 조회·삽입·갱신과 epoch 조회 권한을 준다. `FOR SHARE`에 필요한 epoch 갱신 권한은 `CHECK (lock_guard)`로 참만 허용하는 잠금 전용 열 하나에 한정한다. `product_runtime`·`product_api`·`product_model_writer`에는 새 테이블 권한을 주지 않는다. 이 역할에는 로그인 자격도, 현재 epoch 발급 권한도 없다. 따라서 이 설치 변경은 독립 Host 서비스나 실제 송신 활성화를 뜻하지 않는다. 기존 설치의 DB에는 `update.sh`만으로 이 스키마·역할이 추가되지 않으며, 별도 검증된 설치 관리자 이관 절차가 필요하다.

## 게시 순서

`Recorder`는 새 일반 `e2-http-v3` 저널의 헤더를 Sync한 뒤 `register`를 게시한다. 각 사건은 `Journal.Record`의 Write·Sync를 마친 후, 소유한 파일에서 읽은 스냅샷을 `Store.Publish`에 전달한다. Store는 전체 MAC·체인·마지막 체크포인트와 헤더 키 참조를 검증한다. 추가 게시에서는 저장된 이전 스냅샷의 길이와 digest가 새 스냅샷의 정확한 접두부인지 확인한다. 일회용 SQL 트랜잭션은 현재 epoch를 잠그고 revision을 갱신하며 `synchronous_commit=on`으로 커밋한다. 별도 인증된 `Head` 재조회가 요청한 revision·phase·체크포인트와 일치한 경우에만 ACK를 반환한다. 실패하면 해당 Recorder는 후속 기록을 거부한다. Recorder는 저널 파일과 0700 디렉터리의 inode·소유권을 스냅샷 전후에 대조한다.

`VerifyCurrent`는 저장소의 현재 epoch·head를 조회하고 제공된 저널의 정확한 길이·digest·MAC 체인·키 참조를 확인한 후 head를 다시 조회한다. 이전 head와 이전 스냅샷이 서로 맞더라도 현재 head와 다르면 거부한다. 읽는 동안 head가 바뀌어도 거부한다. `Seal`은 Host가 별도 실행 종료·drain 증거를 확인해야 허용된다.

게시 실패 뒤 로컬 Sync를 마친 사건이 저장된 head보다 더 길게 남으면 그 회차의 저널 전체를 현재 검증 실패로 둔다. 저장된 head의 접두부를 자동으로 E③ 근거나 평가의 완료 기록으로 승격하지 않는다. 별도 검증된 복구 절차가 생기기 전까지 E③ `Sealed`와 평가 v3 수집은 실패로 분류한다.


## 활성화 전 남은 경계

- 설치 관리자가 epoch 및 복원 anchor를 보호 절차로 발급하고, 저널 작성 역할과 현재 head 저장 역할을 분리해야 한다. DB 백업 복원·epoch 변경도 Host lease가 fence해야 한다.
- Host가 실행·철회·정책·저널 독점 소유와 감사 키 매핑을 확인해야 한다. digest 키는 감사 키와 분리해야 한다. 이 패키지는 키를 생성하거나 E③ 영수증을 발행하지 않는다.
- 제품의 준비 서비스·실행 서버에 이 Recorder를 주입하지 않았다. `ErrE3JournalUnavailable` 차단은 유지되며 E③ 프로그램 송신과 VAmPI 검증은 미실행이다.
- 현재 구현은 게시마다 전체 스냅샷을 읽고 MAC을 다시 검증한다. 설정 상한은 256MiB이며, 긴 실행에서의 처리량·메모리 수용은 아직 측정하지 않았다. 제품 연결 전 자원 예산과 증분 검증 방식을 검토해야 한다.
- 이 코어의 readback ACK는 E② 저널 저장 ACK이지 대상 서버의 HTTP 응답이나 E③ 판정 근거가 아니다. 권한 분리와 제품 수명 결속이 검증되기 전에는 송신 활성화 근거로 사용하지 않는다.

## 검증

합성 저널과 일회용 PostgreSQL 16에서 헤더·사건·봉인 게시, 현재 head 검증, 과거 스냅샷, MAC 변경, 오래된 revision, epoch 변경, 조회 중 head 변경, ACK 불명, 스냅샷 파일·디렉터리 교체, typed nil 권위, 게시 권한 철회 및 실패 후 후속 기록 차단을 시험한다. 테스트 fixture의 Host는 가짜 권위이며 운영 권한 발급의 검증 근거가 아니다.
