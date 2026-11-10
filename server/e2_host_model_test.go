package server

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestE2HostModelMissingAuthorityAndBaseline(t *testing.T) {
	for _, call := range []func() error{
		func() error { return CheckFirstDiagnosisModelRoleBinding(nil, nil, 0) },
		func() error { return CheckFirstDiagnosisModelRoleBinding(context.Background(), nil, 1) },
		func() error { return CheckFirstDiagnosisTaskProfileBinding(nil, nil, 1, 1) },
		func() error { return CheckFirstDiagnosisTaskProfileBinding(context.Background(), nil, 1, 0) },
		func() error { return (&Server{firstDiagnosis: true}).bindFirstDiagnosisModel(t.Context(), nil) },
		func() error { return (&Server{firstDiagnosis: true}).checkFirstDiagnosisModel(t.Context(), nil) },
	} {
		if !errors.Is(call(), ErrFirstDiagnosisModel) {
			t.Fatal("missing actual model authority was accepted")
		}
	}
	baseline := &Server{}
	if baseline.bindFirstDiagnosisModel(nil, nil) != nil || baseline.checkFirstDiagnosisModel(nil, nil) != nil {
		t.Fatal("ordinary server acquired a new model boundary")
	}
	pin := &firstDiagnosisModelPin{TaskID: "SYNTHETIC-PRIVATE-REFERENCE", ProfileID: 123}
	for _, format := range []string{"%v", "%+v", "%#v"} {
		if strings.Contains(fmt.Sprintf(format, pin), "PRIVATE") {
			t.Fatal("model pin formatting disclosed inputs")
		}
	}
}
