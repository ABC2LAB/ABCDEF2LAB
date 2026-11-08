package agent

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/e2runner"
)

func TestE2HTTPResultObserverWithholdsOutputAndPrivateFailure(t *testing.T) {
	result := completedE2WorkerResult()
	result.ToolOutput = "private fixture tool output"
	before := result.Trusted
	calls := 0
	got, err := observeE2HTTPBoundResult(context.Background(), func(_ context.Context, observed e2runner.Result) error {
		calls++
		if observed.ToolOutput != "" || !reflect.DeepEqual(observed.Trusted, before) {
			t.Fatal("observer received raw output or changed trusted binding")
		}
		return errors.New("private observer failure fixture")
	}, result, e2runner.ErrUnavailable)
	if calls != 1 || !errors.Is(err, ErrE2HTTPIncomplete) || !errors.Is(err, e2runner.ErrUnavailable) || strings.Contains(err.Error(), "private observer failure") || !reflect.DeepEqual(got, result) {
		t.Fatal("observer failure was lost, disclosed, or changed the actual runner result")
	}
}

func TestE2HTTPResultObserverAbsenceKeepsExistingResult(t *testing.T) {
	result := completedE2WorkerResult()
	got, err := observeE2HTTPBoundResult(context.Background(), nil, result, e2runner.ErrUnavailable)
	if !reflect.DeepEqual(got, result) || !errors.Is(err, e2runner.ErrUnavailable) || errors.Is(err, ErrE2HTTPIncomplete) {
		t.Fatal("absent observer changed runner semantics")
	}
}
