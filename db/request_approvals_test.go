package db

import (
	"context"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRequestApprovalDurableCASOwnedPostgres(t *testing.T) {
	dsn := os.Getenv("ARTEX_REQUEST_APPROVAL_TEST_DSN")
	owned := os.Getenv("ARTEX_REQUEST_APPROVAL_TEST_OWNED") == "1"
	// 저장소 CI가 매 패키지마다 새로 만드는 승인 시험 DB를 재사용한다.
	if dsn == "" && os.Getenv("CI_PG_SOCKET") != "" && os.Getenv("CI_PG_DATABASES") != "" {
		dsn = os.Getenv("ARTEX_INTERCEPT_TEST_DSN")
		owned = dsn != ""
	}
	if dsn == "" {
		t.Skip("승인 시험 전용 PostgreSQL 설정 없음")
	}
	if !owned {
		t.Fatal("소유 시험 DB 확인 필요")
	}
	d, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	task, err := d.CreateTask("synthetic request approval", "fixture", nil, 0, 300)
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(task.ID)
	ctx := context.Background()
	a := RequestApproval{ID: "synthetic-cas", TaskID: task.ID, OwnerRef: "ARTEX", State: "pending", ExpiresAt: time.Now().Add(time.Minute), ExecutionState: "not_sent", InputPreview: map[string]string{"body.public": "value"}}
	if err := d.InsertRequestApproval(ctx, a); err != nil {
		t.Fatal(err)
	}
	loaded, err := d.GetRequestApproval(ctx, task.ID, "ARTEX", a.ID)
	if err != nil || loaded.Version != 1 {
		t.Fatal("durable read failed", err)
	}
	if _, err := d.GetRequestApproval(ctx, task.ID+1, "ARTEX", a.ID); err == nil {
		t.Fatal("cross-task read")
	}
	if _, err := d.GetRequestApproval(ctx, task.ID, "other", a.ID); err == nil {
		t.Fatal("cross-owner read")
	}
	var won atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			candidate := loaded
			candidate.State = "allowed"
			err := d.SwapRequestApproval(ctx, candidate, true)
			if err == nil {
				won.Add(1)
			} else if !errors.Is(err, ErrRequestApprovalConflict) {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if won.Load() != 1 {
		t.Fatalf("decisions=%d", won.Load())
	}
	if err := d.InsertRequestApproval(ctx, a); err == nil {
		t.Fatal("duplicate attempt inserted")
	}
	a.ID = "synthetic-expired"
	a.ExpiresAt = time.Now().Add(-time.Second)
	if err := d.InsertRequestApproval(ctx, a); err != nil {
		t.Fatal(err)
	}
	a, err = d.GetRequestApproval(ctx, task.ID, "ARTEX", a.ID)
	if err != nil {
		t.Fatal(err)
	}
	a.State = "allowed"
	if !errors.Is(d.SwapRequestApproval(ctx, a, true), ErrRequestApprovalConflict) {
		t.Fatal("expired decision accepted")
	}
	a.State = "expired"
	if err := d.SwapRequestApproval(ctx, a, false); err != nil {
		t.Fatal(err)
	}
}

// record의 Go 만료시각은 미래지만 DB 집행 컬럼은 과거인 경우를 고정한다.
func TestRequestApprovalFreshnessUsesDatabaseClock(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	task, err := d.CreateTask("synthetic DB clock", "fixture", nil, 0, 300)
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(task.ID)
	a := RequestApproval{ID: "synthetic-db-clock", TaskID: task.ID, OwnerRef: "ARTEX", State: "allowed", ExpiresAt: time.Now().Add(time.Hour)}
	if err = d.InsertRequestApproval(t.Context(), a); err != nil {
		t.Fatal(err)
	}
	if _, err = d.Exec(`UPDATE request_approvals SET expires_at=NOW()-INTERVAL '1 second' WHERE id=$1`, a.ID); err != nil {
		t.Fatal(err)
	}
	a, err = d.GetRequestApproval(t.Context(), task.ID, "ARTEX", a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !time.Now().Before(a.ExpiresAt) {
		t.Fatal("fixture Go clock is expired")
	}
	a.Consumed = true
	if err = d.SwapRequestApproval(t.Context(), a, true); !errors.Is(err, ErrRequestApprovalConflict) {
		t.Fatalf("DB-expired consume accepted: %v", err)
	}
	a.Consumed = false
	a.State = "expired"
	if err = d.SwapRequestApproval(t.Context(), a, false); err != nil {
		t.Fatal("lifecycle write blocked", err)
	}
}
