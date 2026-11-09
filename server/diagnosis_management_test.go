package server

import (
	"context"
	"errors"
	"testing"

	"github.com/Autumn-27/artex/diagnosis"
	"github.com/Autumn-27/artex/diagnosisapi"
)

type managementTestRegistry struct{}

func (managementTestRegistry) Profiles(context.Context, string) ([]diagnosisapi.Profile, error) {
	return []diagnosisapi.Profile{{ID: "synthetic-profile", Label: "합성 읽기 시험", Configuration: diagnosisapi.Configuration{TargetRef: "owned-loopback", ScopeRef: "read-only", ModelProfileRef: "synthetic-model", ApprovalMode: "confirm_every_request", Budget: diagnosisapi.Budget{Requests: 2, Tokens: 100, DurationSeconds: 60}}}}, nil
}
func (managementTestRegistry) Approval(context.Context, string, diagnosisapi.Draft) (*diagnosisapi.Binding, error) {
	return nil, nil
}

type managementTestGateway struct{ calls int }

func (g *managementTestGateway) Call(context.Context, diagnosis.Request) (diagnosis.Response, error) {
	g.calls++
	return diagnosis.Response{}, errors.New("실행하지 않는 합성 Gateway")
}
func TestDiagnosisManagementRejectsMissingHost(t *testing.T) {
	for _, ctx := range []context.Context{nil, context.Background()} {
		if _, err := NewDiagnosisManagementHandler(ctx, nil, "", "", nil, nil); err == nil {
			t.Fatal("missing host accepted")
		}
	}
}
