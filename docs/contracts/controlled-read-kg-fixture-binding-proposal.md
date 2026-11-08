# 등록 조회와 KG fixture snapshot의 실행 결속 제안

> **단계 기록과 현재 구현:** 아래 선행 미병합 표시는 opt-in 연결의 작성 당시 기록입니다. 현재 [최종 KG Source 결속](controlled-read-kg-source-binding-proposal.md)은 #214·#231·#232의 병합된 연결을 소비하며 [서버 코드](../../server/controlled_read_kg_fixture.go)에 등록 결속이 있습니다. 실제 운영 권위·실모델 수용과 일반 HTTP 통합 완료를 뜻하지 않습니다.

상태: **opt-in 서버 조립 초안 구현**. 기존 [KG registry §9](./kg-fixture-registry.md#9-e②-private-snapshot과의-대응1단계-준비-순서)의 `Parse`·`CheckPairing`을 서버의 실제 등록/실행에 연결한다. 기본 서버나 일반 Bash 실행을 활성화하지 않으며 운영 권위·신원·승인을 발급하지 않는다.

## 범위와 선행

기준 dev는 `210ce51c4332642ff18db46c9075a63523dcc84b`다. 실행의 private Config handle은 미병합 #214의 `8d7d9b91133984f2722cbc897d8261de14c7f367`을 별도 후속 브랜치에 일반 merge한 선행이며 원 PR은 수정하지 않았다. 최종 게시 때 실제 dev·선행 head를 다시 확인한다. #215의 최종 저장 권한 수명은 별도이고, #219·#220의 거절된 통합은 이 구현에 포함하지 않는다.

이 변경은 등록 GET/IDOR의 KG 일관성 경계다. 일반 E② 프록시와 IDOR를 같은 실행으로 조립한 결과, 실제 KG 내용/소유 관계의 인증, 실제 A/B 세션 확인, Docker/모델 수용으로 해석하지 않는다.

## 신뢰 host의 명시적 파일 짝

`ControlledReadHostOptions.FixtureRegistries`는 `snapshot_ref → registry 파일 basename`의 Go map이다. 기존 strict/durable/revisioned 생성자에서 같은 Host 옵션으로 전달하며 복구된 작업·백그라운드 admission 전에 복사·설치한다.

```go
options.FixtureRegistries = map[string]string{
    "approved-read": "prepared-kg.json",
}
// options의 Mode, Findings 및 선택한 durable/revision 공급자는
// 기존 계약대로 실제 신뢰 공급자가 채워야 한다.
```

- map이 nil이면 기존 호환 경로를 유지한다. 비nil 빈 map은 설정 오류다.
- snapshot ref는 기존 opaque ref 규칙, 파일은 기존 private 파일 basename 규칙을 따른다. 경로 이동·절대 경로·숨김 파일·snapshot JSON 자체를 registry로 지정하는 것은 거부한다.
- 파일은 기존 private snapshot 디렉터리 안에 두며 기존 `readPrivate`의 고정 디렉터리·최종 symlink 거부·일반 파일·정확한 `0700`/`0600`·크기 상한을 통과해야 한다. 조상 디렉터리와 허용 writer의 신뢰는 운영자 경계로 남는다.
- 설정 뒤 caller map을 변경해도 설치된 사본은 바뀌지 않는다. pending/active/등록 전환이 시작됐거나 이미 설치한 map은 교체하지 않는다. 실행 중 파일 재탐색·기본 경로·미등록 ref의 nil KG fallback은 없다.
- HTTP/모델 입력과 private snapshot JSON에 새 registry 선택 필드를 추가하지 않는다. 어떤 registry가 어떤 snapshot ref에 대응하는지는 신뢰 host가 명시한다.

## 등록과 실제 소비

기존 `store.load`가 private E② snapshot을 읽은 뒤 registry를 제한 크기로 읽고 `kgfixture.Parse`로 동결한다. 같은 E② 입력의 session label 전체·reset ref·resource ref 전체·view marker(field/value/trivial)를 `kgfixture.Pairing`에 투영해 `Snapshot.CheckPairing`을 호출한다. view가 없으면 marker는 없는 것으로 대조한다. 소유 관계는 재구현하지 않는다. E②의 accounts 허용 목록을 소유로 해석하지 않는다.

누락·파일 오류·parse/pairing 불일치는 pending 게시 전에 거부하며 반환 spec에 키나 snapshot을 남기지 않는다. 새 KG 거부 경로에서는 읽은 audit key 사본을 지운다. 기존 task marker/DB fence의 downgrade 방지는 유지한다. store 경계의 KG 오류는 기존 고정 코드뿐이고 HTTP·모델에는 기존 unavailable 의미가 유지된다.

동결 pointer는 pending spec → 실제 lease → #214의 private Config handle에 그대로 연결된다. 등록 뒤 원래 snapshot/registry 파일을 바꿔도 이미 등록된 실행은 다시 읽지 않는다. 새 등록은 별도 snapshot 수명을 가진다.

신뢰 resolver는 실제 `ControlledReadVerificationRequest.FixtureSnapshot(ctx)`에서 동결 snapshot을 얻고 기존 `BindRealInputs`와 Source 조립에 사용한다. 요청의 public field만 복사하거나 JSON으로 복구해서 handle을 만들 수 없다. 실제 task/intent/scope/account/resource, 취소·최초 만료, close·request pin 검사와 KG 무효 상태를 다시 확인한다. 이 메서드의 반환값은 신뢰 Go host용이며 canary·파일·세션 내용을 LLM이나 공유 로그에 넣지 않는다.

opt-in 실행의 `BindRealInputs` 및 실제 verification은 이 등록의 **같은 pointer**만 받는다. 동일 JSON을 다시 파싱해 같은 digest를 얻은 다른 snapshot, 다른 resolver 구현, typed nil 또는 무효 snapshot은 송신 전에 거부한다. Source·판정/저장의 실제 출처·문맥 검증은 기존 경계가 계속 담당한다.

## 수명과 한계

close 시작 시 Config handle을 닫아 새 snapshot 조회를 먼저 차단한다. 이미 선택한 검증이 보유한 KG는 기존 감사 마감·finalization callback이 반환할 때까지 보존하고, close의 모든 종료 경로에서 그 뒤 `Invalidate`한다. 새 등록의 별도 snapshot은 영향을 받지 않는다.

이 수명은 host lease·root/epoch/key 권한의 대체물이 아니다. `Snapshot.Invalidate`만으로 진행 중인 네트워크를 강제 종료한다는 보장은 추가하지 않는다. 실제 회수/중지는 기존 전송 context·StopAndWait·host 수명 연결을 따르며 #215의 최종 DB 저장 권한 수용을 이 변경으로 대체하지 않는다.

초안의 opt-in 조립은 검토 대상이다. 실제 운영자의 검증된 준비 파일·발급/수명/권한 공급·일반 E②와의 같은 실행 조립·제품 배치·실모델 수용은 후속이다. 시험 fixture의 합성 계정·권위·대상은 실제 준비 자료가 아니다.

## 검증 범위

합성 private 파일의 pairing 각 축·미등록 map·형식/크기/권한/symlink 거부, 실패 등록의 marker 보존·실행 차단, nil map 호환성과 map 사본/교체 거부를 시험한다. 실제 원본 Worker→소유 loopback HTTP owner/cross 두 조회→감사 마감→finalization과 등록 뒤 입력 파일 교체, 같은 내용의 별도 snapshot 거부, request/취소/만료 결속, finalization 전후의 snapshot 수명을 대조한다. 실제 DB 저장·운영 bootstrap·대상 스캔·유료 API·모델·배포 수용은 이 fixture의 완료 주장이 아니다.
