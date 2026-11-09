package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Autumn-27/artex/e3record"
	"github.com/Autumn-27/artex/evidencebind"
	"github.com/Autumn-27/artex/kgfixture"
	"github.com/Autumn-27/artex/oracle"
	"github.com/golang-jwt/jwt/v5"
)

// 각 필수 근거를 따로 훼손해야 다른 정상 조건이 누락된 가드를 가리지 않는다.
func TestAccessMapProjectionRequiredEvidence(t *testing.T) {
	in, source := finalizationRecordFixture()
	prepared, err := e3record.Prepare(t.Context(), in, func(context.Context, evidencebind.Context) (e3record.Source, error) { return source, nil })
	if err != nil {
		t.Fatal(err)
	}
	snapshot, ok := prepared.Snapshot()
	if !ok {
		t.Fatal("합성 판정 준비 실패")
	}
	var confirmed accessMapRecord
	if err := json.Unmarshal(snapshot.Canonical, &confirmed); err != nil {
		t.Fatal(err)
	}
	t.Run("canonical-policy-time", func(t *testing.T) {
		s := source
		s.Policy.ValidFrom = s.Policy.ValidFrom.Add(123 * time.Nanosecond)
		s.Policy.ValidUntil = s.Policy.ValidUntil.Add(123 * time.Nanosecond)
		if !confirmed.valid(s) {
			t.Fatal("같은 저장 정밀도의 정책 시각을 손상으로 표시함")
		}
		s.Policy.ValidFrom = s.Policy.ValidFrom.Add(time.Microsecond)
		if confirmed.valid(s) {
			t.Fatal("저장 정밀도를 넘는 정책 시각 차이를 허용함")
		}
	})
	allowed := confirmed
	allowed.OracleValue, allowed.VulnVerdict, allowed.AccessPolicyResult = oracle.Allowed, nil, "위반_미관찰"
	allowed.ExpectedAccess = oracle.AccessAllow
	b := accessMapBinding{Principals: []string{confirmed.EvaluatedRef}, Resources: []kgfixture.MapResource{{ResourceRef: confirmed.ResourceRef, OwnerRef: confirmed.OwnerRef}}}
	t.Run("owner-control-with-policy", func(t *testing.T) {
		ownerBinding := accessMapBinding{Principals: []string{confirmed.OwnerRef, confirmed.EvaluatedRef}, Resources: []kgfixture.MapResource{{ResourceRef: confirmed.ResourceRef, OwnerRef: confirmed.OwnerRef, Principals: []string{confirmed.OwnerRef, confirmed.EvaluatedRef}}}, Attempts: []accessMapAttempt{{Principal: confirmed.EvaluatedRef, Resource: confirmed.ResourceRef}}}
		response := projectAccessMap(ownerBinding, []accessMapStoredRecord{{id: 1, record: confirmed, valid: true}})
		owner := accessMapFind(t, response, confirmed.OwnerRef)
		if owner.State != "unknown" || owner.ProjectionError == nil || *owner.ProjectionError != "observation_unavailable" {
			t.Fatal("소유자 정상 대조를 판정 대상 누락으로 표시함")
		}
	})
	for _, tc := range []struct {
		name string
		base accessMapRecord
		edit func(*accessMapRecord)
		want string
	}{
		{"confirmed-positive", confirmed, func(*accessMapRecord) {}, "confirmed"},
		{"confirmed-policy", confirmed, func(r *accessMapRecord) { r.PolicyCheck = oracle.StateUnknown }, "unknown"},
		{"confirmed-identity", confirmed, func(r *accessMapRecord) { r.IdentityCheck = oracle.StateUnknown }, "unknown"},
		{"confirmed-data", confirmed, func(r *accessMapRecord) { r.DataCheck = oracle.StateUnknown }, "unknown"},
		{"confirmed-candidate", confirmed, func(r *accessMapRecord) { r.AccessPolicyResult = "판정_불가" }, "unknown"},
		{"allowed-positive", allowed, func(*accessMapRecord) {}, "allow-ok"},
		{"allowed-resource", allowed, func(r *accessMapRecord) { r.SameResource = oracle.StateUnknown }, "unknown"},
		{"allowed-cross", allowed, func(r *accessMapRecord) { r.CrossNormalData = oracle.StateUnknown }, "unknown"},
		{"allowed-identity", allowed, func(r *accessMapRecord) { r.IdentityCheck = oracle.StateUnknown }, "unknown"},
		{"allowed-subject", allowed, func(r *accessMapRecord) { r.EvaluatedStatus = oracle.SubjectUnknown }, "unknown"},
		{"allowed-canonical", allowed, func(r *accessMapRecord) { r.HintMatchesCanonical = false }, "unknown"},
		{"allowed-before-validity", allowed, func(r *accessMapRecord) { r.ObservedAt = r.PolicyValidFrom.Add(-time.Microsecond) }, "unknown"},
		{"allowed-at-expiry", allowed, func(r *accessMapRecord) { r.ObservedAt = r.PolicyValidUntil }, "unknown"},
		{"allowed-without-permission", allowed, func(r *accessMapRecord) {
			r.ExpectedAccess = oracle.AccessDeny
			r.Sharing = oracle.ResolvedNone
			r.Delegation = oracle.ResolvedNone
		}, "unknown"},
		{"allowed-by-sharing", allowed, func(r *accessMapRecord) { r.ExpectedAccess = oracle.AccessDeny; r.Sharing = oracle.ResolvedAllow }, "allow-ok"},
		{"allowed-by-delegation", allowed, func(r *accessMapRecord) { r.ExpectedAccess = oracle.AccessDeny; r.Delegation = oracle.ResolvedAllow }, "allow-ok"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := tc.base
			tc.edit(&r)
			c := projectAccessMap(b, []accessMapStoredRecord{{id: 1, record: r, valid: true}}).Cells[0]
			if c.State != tc.want {
				t.Fatalf("필수 근거 훼손 뒤 state=%s want=%s", c.State, tc.want)
			}
			if tc.base.OracleValue == oracle.Confirmed && tc.want == "unknown" && (c.ProjectionError == nil || *c.ProjectionError != "record_invalid") {
				t.Fatal("잘못된 확정 근거를 판정 원값과 구분하지 못함")
			}
		})
	}
}

func TestAccessMapDirectAuthenticationOwnedPostgres(t *testing.T) {
	f := newAccessMapTestFixture(t)
	f.save(t)
	s := &Server{m: &Manager{pg: f.pg}, jwtKey: []byte("synthetic-direct-map-signing-key")}
	hash, err := s.reloadPassHash()
	if err != nil {
		t.Fatal(err)
	}
	sign := func(subject string) string {
		t.Helper()
		token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.RegisteredClaims{Subject: subject, ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Minute))}).SignedString(s.signingKey(hash))
		if err != nil {
			t.Fatal(err)
		}
		return token
	}
	valid := sign("ARTEX")
	for _, tc := range []struct {
		name, auth string
		want       int
	}{
		{"valid-bearer", "Bearer " + valid, 200},
		{"missing-bearer", "", 401},
		{"wrong-scheme", "Basic " + valid, 401},
		{"bare-token", valid, 401},
		{"empty-bearer", "Bearer ", 401},
		{"foreign-subject", "Bearer " + sign("other-subject"), 401},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/api/tasks/"+f.b.Scope.TaskRef+"/access-map", nil)
			r.Header.Set("Authorization", tc.auth)
			w := httptest.NewRecorder()
			// 바깥 인증 미들웨어가 핸들러 자체의 가드 누락을 가리지 않게 한다.
			s.serveAccessMap(w, r, f.b.TaskID)
			if w.Code != tc.want || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("직접 핸들러 인증 status=%d want=%d", w.Code, tc.want)
			}
		})
	}
}

func TestAccessMapRegistrationGenerationGuardsOwnedPostgres(t *testing.T) {
	for _, scenario := range []string{"register-current", "register-stale", "register-ready", "attempt-current", "attempt-stale", "attempt-scope"} {
		t.Run(scenario, func(t *testing.T) {
			f := newAccessMapTestFixture(t)
			isRegister := scenario == "register-current" || scenario == "register-stale" || scenario == "register-ready"
			if isRegister && scenario != "register-ready" {
				if err := f.store.update(t.Context(), f.b.TaskID, func(b *accessMapBinding) error {
					b.Ready = false
					if scenario == "register-stale" {
						b.Registration = "new-registration"
					}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			}
			l := &controlledReadLease{runtime: &controlledReadRuntime{mapStore: f.store}, scope: f.b.Scope, mapRegistration: f.b.Registration}
			if scenario == "attempt-stale" {
				l.mapRegistration = "old-registration"
			}
			if scenario == "attempt-scope" {
				l.scope.RunRef = "other-run"
			}
			var err error
			if isRegister {
				err = f.store.register(t.Context(), f.spec)
			} else {
				err = l.mapAttempt(t.Context(), f.b.Resources[0].ResourceRef, "reader")
			}
			positive := scenario == "register-current" || scenario == "attempt-current"
			if positive && err != nil || !positive && !errors.Is(err, errAccessMapScope) {
				t.Fatal("등록·시도의 세대 불일치를 거부하지 않음", err)
			}
		})
	}
}

func TestAccessMapVerificationAttemptFailurePrecedesResolverOwnedPostgres(t *testing.T) {
	f := newAccessMapTestFixture(t)
	l := &controlledReadLease{runtime: &controlledReadRuntime{mapStore: f.store}, scope: f.b.Scope, mapRegistration: "stale-registration"}
	resolved := false
	v := &controlledReadVerificationState{runCtx: t.Context(), host: &controlledReadVerificationConfig{lifecycle: t.Context(), resolve: func(context.Context, ControlledReadVerificationRequest) (ControlledReadVerificationPlan, error) {
		resolved = true
		return ControlledReadVerificationPlan{}, errControlledReadUnavailable
	}}}
	if _, err := v.execute(t.Context(), l, "reader", f.b.Resources[0].ResourceRef); !errors.Is(err, errControlledReadUnavailable) || resolved {
		t.Fatal("시도 저장 실패 뒤 검증 resolver를 실행함")
	}
}
