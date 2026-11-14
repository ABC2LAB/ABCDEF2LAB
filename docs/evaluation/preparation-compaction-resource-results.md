# C34 준비 저널 압축·복원 검증

검증일: 2026-11-14(KST). 구현 기준: `a4ddc1829c5eb53ab1cc0717f9a31bf0f81de9c5`.
결정: [C34·C35 결정안 PR #566](https://github.com/ABC2LAB/ABCDEF2LAB/pull/566)의 C34 3.
선행: [C04·C33 PR #559](https://github.com/ABC2LAB/ABCDEF2LAB/pull/559)의 실행 반환·복구 증거 필드를 보존한다. `0408f4e8`을 merge했으며 #559가 먼저 병합되어야 한다.

## 지원 상한과 적용 범위

| 항목 | 기존 | 제안 구현 | 근거·유지 조건 |
|---|---:|---:|---|
| 준비 작업 수 | 1,024 | 8,192 | 실제 8,192개 복원·8,193개 거절 및 신규 생성 거절 시험 |
| 준비 저널 입력 | 32MiB | 128MiB | 입력 파일 크기를 읽기 전에 검사, 한 레코드 128KiB 유지 |
| 대상·계정·접촉 등 일반 보호 저널 | 32MiB | 32MiB | 일반 API는 별도 상한을 사용하며 압축 형식을 수용하지 않음 |

8,192와 128MiB는 동시에 적용하는 유한 제품 상한이다. 작업별 레코드 크기가 다르므로 모든 8,192개 조합이 반드시 128MiB에 들어간다는 보장은 없다. 조직의 요청·시간·비용 예산과 실행 소유권 제한은 그대로 적용한다. 상한 변경은 동료 코드 리뷰 대상이다.

이 PR은 **준비 작업 저널**만 압축한다. `targets/preparation.journal`의 철회·만료·revision 기록도 계속 누적되고 32MiB 상한에서 닫힌다. 대상의 활성 자리 재사용은 저장 용량 무제한을 뜻하지 않는다. 대상 저널의 장기 압축은 원본 승인·철회·계정 연결 이력을 보존하는 별도 계약과 시험이 필요하다. 보관·삭제 API나 설치 이미지 지문 마이그레이션은 추가하지 않는다.

## 저장·복구 계약

기동 시 기존 chain·head·파일 소유자·권한·inode/link·영수증·상태 전이를 전부 검증한다. 작업마다 마지막 영수증을 정렬해 사설 임시 디렉터리에 기록하고 data/head/디렉터리를 fsync한다. 후보를 같은 저장·영수증 검증기로 다시 읽고, 원본 sequence/digest 및 전체 작업 map과 일치해야 채택한다. 작업·예약·실행 횟수·Unknown·실행 반환·drain·task/보고서 참조는 지우지 않는다. E③ 판정·감사·증거 파일은 수정하지 않는다.

`preparation.current/` 아래 data/head를 한 세대로 묶는다. 첫 채택은 디렉터리 rename, 다음 채택은 Linux `RENAME_EXCHANGE`로 교환하고 부모를 fsync한다. 부모 루트 flock은 교환 중에도 유지된다. 게시 전 실패는 옛 data/head를 바이트 그대로 보존하고 기동을 거절한다. 게시 후 동기화·호환 가드 실패는 현재 writer를 중단하며, 재기동은 검증된 전체 세대만 읽는다. 정리 실패는 고정 문구로 알리고 옛 세대를 남긴다.

새 형식은 `diagnosis-preparation-snapshot.v2`이며 원본 chain의 끝을 헤더에 보존한다. 과거 평면 head는 구버전 reader가 오래된 회계로 재개하지 못하도록 가드로 닫는다. 정상 압축 뒤 구버전으로의 직접 downgrade는 지원하지 않는다. 업데이트·복구에는 이 형식을 읽는 제품을 사용해야 하며, 불완전 게시 뒤 구버전으로 강제 rollback하는 경로를 제공하지 않는다. 전체 설치 디렉터리를 임의로 과거 복사본으로 되돌리는 공격에 대한 외부 rollback 방지 장치는 이 변경의 범위가 아니다.

## 자원 측정

환경: Linux x86_64, Go 1.26.3, 로컬 파일시스템. 테스트 프로세스 1개, 네트워크·DB·실제 모델 호출 없음. 합성 fixture에는 작업당 유효한 영수증 3개와 긴 허용 경로 9개를 넣었다. 비밀·실제 진단 출력은 사용하지 않았다.

```sh
ARTEX_PREPARATION_RESOURCE_TEST=1 go test -json ./diagnosisprep \
  -run 'TestCompaction(MaximumSupportedWorkload|ResourceFixtureAtSmallScale)' -count=1
```

| 측정 | 관측값 |
|---|---:|
| 작업 수 | 8,192 |
| 입력 파일 | 121,181,507 bytes (115.6MiB) |
| 압축 뒤 파일 | 41,057,491 bytes (39.2MiB) |
| 복원·검증·압축 경과 | 20,900ms |
| 구간 누적 할당 | 6,755,927,416 bytes |
| 복원 반환 직후 live heap | 153,813,160 bytes |
| 프로세스 최고 RSS(VmHWM) | 541,968KiB (약 529MiB) |

비계측 production 경로의 이 fixture는 1분 constructor context 안에서 성공했다. 누적 할당은 동시에 살아 있는 메모리가 아니다. VmHWM은 fixture 생성도 포함한 전체 시험 프로세스 수치다. 이 한 번의 관측은 모든 하드웨어·최대 레코드 조합의 시간/RSS 상한 보장이 아니다. 입력 개수·bytes는 코드로 제한하며 OS 메모리 제한·저장 여유는 설치 운영 정책에서 확보해야 한다. 이전 32MiB 구현은 이 입력을 거절하므로 동일 입력 대비 속도 향상 비율은 주장하지 않는다.

같은 대용량 fixture에 `-race`를 적용한 첫 측정은 1분 context 안에 끝나지 않아 **FAIL**이었다. 제한 시간을 늘려 성공으로 바꾸지 않았다. 별도 비계측 측정을 위에 기록했고, 일반 race 회귀시험에서는 이 무거운 측정 1개를 opt-in으로 제외한다. 소규모 동일 의미 fixture와 8,192/8,193개 경계·35MiB 입력/128MiB 초과 조기 거절은 일반 race 시험에 포함된다.

## 기능·음성 검증

- 준비 서비스 전체 race: PASS 이벤트 177, FAIL 0, SKIP 1(대용량 opt-in 측정). 빌드·vet·darwin/arm64·windows/amd64 빌드 통과.
- Unknown/drain·모델 예약·이전 실행 횟수·동시 실행 차단·재시작·보고서/task 소유권을 압축 전후 대조했다. backend Execute 재실행은 0이다.
- 쓰기·fsync·후보 재검증·게시 전 9지점 오류 주입에서 원본 바이트가 보존됐다. 게시 후 부모 동기화·구버전 가드 실패는 writer 중단 뒤 재기동으로 복원했다. 실제 전원 차단 시험은 NOT_RUN이다.
- 후보 source anchor 교체·다른 source/중복/누락 영수증·head/data 손실·권한/심볼릭 링크/하드링크/부분 쓰기·구버전 reader를 음성 시험했다.
- 정상 커밋 뒤 별도 Go overlay 변이 **7/7 검출**: 후보 영수증 검증 제거, 원본 chain anchor 비교 제거, 구버전 reader 가드 제거, 개수 상한을 1,024로 되돌림, replay 개수 제한 제거, 신규 생성 개수 제한 제거, 파일 크기 조기 검사 제거. 모두 컴파일 실패가 아닌 해당 독립 시험의 assertion 실패로 검출했다. 원본 코드는 수정하지 않았다.
- 제품 명령 패키지 전체 race: PASS 이벤트 324, SKIP 3, FAIL 0. 서버 전체 race: PASS 이벤트 1,759, SKIP 305, 시험 FAIL 1(패키지도 FAIL). `TestLocalOnlyModelListDoesNotSendFormOrStoredKey`가 전용 PostgreSQL 부재로 실패했다. 환경 의존 시험을 통과로 계산하지 않는다.
- 별도 제품 관리 중계·입구 race: PASS 이벤트 42, SKIP 1, FAIL 0. 인증·task 결속·허용 라우트·재시도 금지·크기 제한·권한 재검사 회귀를 통과했다. 위 숫자는 `go test -json`의 pass 이벤트이며 패키지/상위 subtest 이벤트를 포함한다.

이 문서는 파일 저장·복원 수용 근거다. 실제 Qwen, 대상 진단, 브라우저 U01의 근거로 사용하지 않는다(이 검증에서 모두 NOT_RUN).
