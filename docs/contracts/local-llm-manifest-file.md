# 운영자 pin manifest 파일 로더

상태: Linux에서 신뢰된 host가 지정한 파일을 읽어 기존 순수 파서에 전달하는 독립 코어. 서버 설정·profile 선택·provider 생성·재로드는 아직 연결하지 않는다.

## 입력과 파일 신뢰

`llmpolicy.LoadProfileManifestFile(path)`는 host 설정에서 받은 절대 경로를 요구한다. 상대 경로, `.`·`..`·중복 구분자 같은 비정규 경로는 거부한다. 경로 구성요소를 Linux `openat`의 no-follow 방식으로 순서대로 열어 심볼릭 링크를 거부한다.

manifest의 **바로 위 디렉터리**는 실행 프로세스의 effective UID가 소유하고 정확히 `0700`이어야 한다. 파일은 같은 UID가 소유하는 일반 파일이며 정확히 `0600`, 하드링크 수 1이어야 한다. 로더는 실제 운영 파일에 `chmod`·`chown`을 수행하지 않는다. 배포자가 그 권한과 소유권을 준비해야 한다. 상위 디렉터리의 모드는 이 로더가 검사하지 않지만 각 구성요소가 symlink가 아니고 열린 경로와 같은 inode인지 읽기 뒤 재확인한다.

파일을 읽기 전에 크기 1..1,048,576바이트를 확인하고, 읽기 자체도 최대 크기보다 1바이트만 더 읽도록 제한한다. 열린 파일과 해당 경로의 장치·inode·권한·소유자·링크 수·크기·수정 시각·상태 변경 시각을 읽기 전후 비교한다. 같은 UID의 운영자가 파일을 **제자리에서** 동시에 수정하는 동안의 완전한 불변 스냅샷까지 보장하는 것은 아니므로, 배포·교체는 새 파일을 준비해 원자적으로 바꾸고 로더 완료 후에 다음 교체를 진행해야 한다.

## 결과와 연결 경계

파일 내용은 [엄격 JSON v1 파서](./local-llm-manifest.md)에 그대로 전달한다. 성공하면 완성된 `*ProfileResolver`만 반환한다. 경로·권한·읽기·파싱 실패는 모두 `nil`과 고정 `ErrInvalidProfileManifestFile`이며 원본 경로, pin, 모델 값 또는 기저 오류를 담지 않는다. Linux 이외 플랫폼은 같은 고정 오류로 닫힌다.

이 함수는 파일 출처와 내용을 검증해 순수 resolver를 구성할 뿐, 기존 서버에 설치하거나 이전 provider를 무효화하지 않는다. 실제 host는 local-only 적용/끔의 신뢰 설정, effective profile/config snapshot, resolver와 provider의 동일 설정 결속, 재로드 실패 시 이전 정책·provider 차단을 별도로 구현해야 한다. 실패를 외부 provider나 다른 profile로 넘기지 않는다.

## 검증

합성 파일로 성공한 pin의 `Resolve`/`Decide`, 원본 변경 후 resolver 불변성, 상대·비정규·누락 경로, 파일·디렉터리 symlink, 권한 과다, 하드링크, 비일반 파일, 빈 파일·최대 크기·초과 크기, 파서 실패와 오류 원문 비노출을 검사한다. 실제 운영 manifest·DB/profile·provider·모델 호출은 시험하지 않는다.
