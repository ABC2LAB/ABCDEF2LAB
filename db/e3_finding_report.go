package db

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Autumn-27/artex/e3record"
	"github.com/Autumn-27/artex/evidencebind"
	"github.com/Autumn-27/artex/oracle"
)

// 확정 저장 경로만 호출한다. 자유문장 대신 불변 manifest 위치와 숫자로 실제 단계를 연결한다.
func controlledFindingReport(s e3record.Snapshot, out ControlledFindingResult) (string, error) {
	if s.Value != oracle.Confirmed || out.Value != oracle.Confirmed ||
		out.ManifestID <= 0 || out.VerdictID <= 0 || out.IssuanceID <= 0 ||
		sha256.Sum256(s.Manifest) != s.ManifestDigest {
		return "", ErrControlledFindingInput
	}
	var manifest struct {
		Version  int64
		Evidence evidencebind.Input
	}
	if err := json.Unmarshal(s.Manifest, &manifest); err != nil || manifest.Version != 1 {
		return "", ErrControlledFindingInput
	}
	checked := evidencebind.Check(s.Source.Expected, manifest.Evidence)
	if !checked.BindingValid || checked.Recomputed.Value != oracle.Confirmed {
		return "", ErrControlledFindingInput
	}
	owner, cross := manifest.Evidence.Owner, manifest.Evidence.Cross
	return fmt.Sprintf(`## 판정과 영향

수평 IDOR 조회가 확인되었습니다. 등록 정책상 허용되지 않은 다른 계정이 같은 자원의 비민감 표식 데이터를 조회했습니다. 관찰한 자원과 당시 조건에 한정하며 다른 자원·쓰기 권한·현재 상태는 검증하지 않았습니다.

## 사전 조건

같은 실행·reset 세대의 등록 자원, 서로 다른 검증된 소유자/조회자 계정, 유효한 접근 정책과 공유·위임 확인, 승인된 GET 범위가 사용되었습니다. 구체적인 계정·자원·정책 참조는 아래 manifest의 각 관찰에 있는 Context에서 확인합니다. 보고서 자체는 재실행 승인이 아닙니다.

## 실제 수행 단계와 기대/관찰

1. 소유자 정상 조회: GET 시도 %d, 관찰 시각 %s, 응답 상태 %d. 정상 소유자 데이터를 확인했습니다. 근거: manifest #%d / Evidence.Owner (Context, Receipt, Evidence).
2. 다른 계정 교차 조회: GET 시도 %d, 관찰 시각 %s, 응답 상태 %d. 기대 결과는 등록 정책에 따른 접근 거부이며, 실제로 같은 자원의 비자명한 표식 데이터 일치를 확인했습니다. 근거: manifest #%d / Evidence.Cross (Context, Receipt, Evidence).
3. 프로그램 판정: 같은 자원·정상 대조·주체 구분·정책 거부를 확인하고 단순 반사와 공통 화면을 제외했습니다. HTTP 상태 코드만으로 확정하지 않았습니다. 근거: manifest #%d / Evidence.Result 및 판정 #%d.

## 증거와 버전

- 판정 #%d, 발급 #%d, manifest #%d
- 실행 증거 revision: %d (finding 증거 버전과 별도)
- 초기 finding 증거 버전: 1, 보고서 근거 버전: 1
- manifest SHA-256: %x

저장된 관찰·영수증·구조화 판정의 요약입니다. 원문 요청/응답, 실행 명령, URL, 비밀 세션값, 브라우저 캡처는 포함하지 않습니다. 스크린샷과 요청·응답 증거 화면은 생성하지 않았습니다. 원문 증거의 보존·조회 가능 여부를 이 요약만으로 보장하지 않습니다.

## 수정 권고

대상 객체를 조회하는 서버 경계에서 인증 주체의 소유권·역할·공유·위임 권한을 검사하고 기본 거부를 적용하세요. 객체 ID 난수화나 화면 숨김만으로 접근 통제를 대신하지 마세요. 구체적인 코드 위치는 별도 조사해야 합니다.

## 수정 후 재검증

새 실행의 정책·권한·세션·예산·감사를 확인하고 필요한 승인을 다시 받은 뒤, 소유자 정상 조회와 다른 계정의 거부를 같은 자원·세대에서 대조하세요. 새 근거와 판정을 원래 finding에 별도로 연결하세요. 수정 후 재검증은 미실행이며 차단·오류·증거 부족을 수정 완료로 간주하지 않습니다.

## 범위와 미검증

등록 GET 수평 IDOR만 지원합니다. 일반 HTTP 후보의 신뢰 판정, 쓰기·수직 IDOR, 스크린샷, 실제 모델 성능은 완료 범위가 아닙니다. 상세 보고서는 권한 있는 조회용이며 공유용 내보내기는 기존 DLP 사본을 사용합니다.
`, owner.Receipt.Attempt, owner.ObservedAt.UTC().Format(time.RFC3339Nano), owner.Receipt.StatusCode, out.ManifestID,
		cross.Receipt.Attempt, cross.ObservedAt.UTC().Format(time.RFC3339Nano), cross.Receipt.StatusCode, out.ManifestID,
		out.ManifestID, out.VerdictID, out.VerdictID, out.IssuanceID, out.ManifestID, s.Source.EvidenceRevision, s.ManifestDigest), nil
}
