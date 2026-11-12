package diagnosisflow

import (
	"errors"
	"strconv"
	"testing"

	"github.com/Autumn-27/artex/e2proxy"
)

// 요청 관찰 투영 실패 격리(C13) 시험이다. 허용 판단 상태의 저장 실패는 계속 전부 막는다.

func projectionReceipt(id int) e2proxy.Receipt {
	return e2proxy.Receipt{AttemptID: "attempt-" + strconv.Itoa(id), CallRef: "owned-call", Decision: "allowed", ForwardAttempted: true, RequestWritten: true, ResponseReceived: true, ResponseComplete: true, StatusCode: 200}
}

func setStoreFail(store *memoryStore, fail bool) {
	store.mu.Lock()
	store.fail = fail
	store.mu.Unlock()
}

func persistedSnapshot(store *memoryStore) Snapshot {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.records["prep-owned"].Snapshot
}

var projectionView = e2proxy.RequestView{Method: "GET", URL: "https://owned.test/read"}

func TestObservationProjectionFailureDoesNotFenceLaterRequests(t *testing.T) {
	c, store := flowFixture(t, Manual)
	if err := c.Before(t.Context(), projectionView); err != nil {
		t.Fatal("fixture request was not admitted", err)
	}
	setStoreFail(store, true)
	if err := c.After(t.Context(), projectionView, projectionReceipt(1)); !errors.Is(err, ErrProjection) {
		t.Fatal("failed observation write was reported as stored", err)
	}
	setStoreFail(store, false)
	if c.Failed() {
		t.Fatal("one deferred observation write failed the workflow")
	}
	if err := c.Before(t.Context(), projectionView); err != nil {
		t.Fatal("one observation write failure fenced a later approved request", err)
	}
	if !c.PreapprovedRead(projectionView) {
		t.Fatal("deferred observation write removed the read exemption")
	}
	if err := c.After(t.Context(), projectionView, projectionReceipt(2)); err != nil {
		t.Fatal("next observation write after recovery failed", err)
	}
	persisted := persistedSnapshot(store)
	if persisted.Counts.Attempted != 2 || persisted.Counts.Sent != 2 {
		t.Fatalf("deferred observation was not rewritten with the next save: %+v", persisted.Counts)
	}
	if current, _ := c.Snapshot(); current.Revision != persisted.Revision {
		t.Fatal("memory and stored revisions diverged after recovery", current.Revision, persisted.Revision)
	}
}

func TestPersistentObservationProjectionFailureFencesAdmission(t *testing.T) {
	c, store := flowFixture(t, Manual)
	// 성공한 저장은 연속 실패 수를 0으로 돌린다.
	setStoreFail(store, true)
	for i := 1; i < maxObservationProjectionFailures; i++ {
		_ = c.After(t.Context(), projectionView, projectionReceipt(i))
	}
	setStoreFail(store, false)
	if err := c.After(t.Context(), projectionView, projectionReceipt(maxObservationProjectionFailures)); err != nil || c.Failed() {
		t.Fatal("successful save did not reset the failure streak", err)
	}
	setStoreFail(store, true)
	for i := 0; i < maxObservationProjectionFailures; i++ {
		_ = c.After(t.Context(), projectionView, projectionReceipt(100+i))
	}
	setStoreFail(store, false)
	if !c.Failed() {
		t.Fatal("persistent projection loss did not fail the workflow")
	}
	if c.Before(t.Context(), projectionView) == nil || c.PreapprovedRead(projectionView) {
		t.Fatal("persistent projection loss kept admitting requests")
	}
	if err := c.After(t.Context(), projectionView, projectionReceipt(200)); !errors.Is(err, ErrProjection) {
		t.Fatal("poisoned controller accepted a new observation")
	}
}

func TestObservationRevisionConflictFencesAdmission(t *testing.T) {
	c, store := flowFixture(t, Manual)
	// 다른 기록이 같은 작업을 먼저 고쳤다(또는 결과를 모르는 커밋이 실제로 반영됐다).
	store.mu.Lock()
	r := store.records["prep-owned"]
	r.Snapshot.Revision++
	store.records["prep-owned"] = r
	store.mu.Unlock()
	if err := c.After(t.Context(), projectionView, projectionReceipt(1)); !errors.Is(err, ErrProjection) {
		t.Fatal("revision conflict was reported as stored", err)
	}
	if !c.Failed() || c.Before(t.Context(), projectionView) == nil {
		t.Fatal("revision conflict did not fence admission")
	}
}
