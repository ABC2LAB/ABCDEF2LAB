package e2proxy

import (
	"context"
	"errors"
	"reflect"
)

// ErrBoundaryProjection reports loss of optional workflow projection, not loss of
// the durable E2 wire audit. A boundary may defer a bounded number of request
// observation writes, but once projection and admission state may diverge it must
// refuse all subsequent admission.
var ErrBoundaryProjection = errors.New("workflow_projection_failed")

// RequestView는 기존 E② 정규화·고정 범위 검사 뒤의 추가 제한 입력이다.
// 잠재적 비밀인 URL을 일반 JSON/로그로 출력하지 않는다.
type RequestView struct {
	Method      string `json:"-"`
	URL         string `json:"-"`
	BodyBytes   int    `json:"-"`
	BodyPresent bool   `json:"-"`
}

func (RequestView) String() string { return "e2proxy.RequestView[protected]" }

// RequestBoundary는 신뢰 호스트만 조립한다. 기존 범위를 넓히거나 송신 내용을 변경할 수 없다.
// Before/After 실패는 차단/감사 실패이며, 자동 재전송 허가가 아니다.
type RequestBoundary interface {
	Before(context.Context, RequestView) error
	PreapprovedRead(RequestView) bool
	After(context.Context, RequestView, Receipt) error
}

func nilRequestBoundary(b RequestBoundary) bool {
	if b == nil {
		return true
	}
	v := reflect.ValueOf(b)
	switch v.Kind() {
	case reflect.Pointer, reflect.Func, reflect.Interface, reflect.Map, reflect.Slice, reflect.Chan:
		return v.IsNil()
	}
	return false
}
