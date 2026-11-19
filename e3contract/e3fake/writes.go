package e3fake

import (
	"context"
	"sync"

	"github.com/Autumn-27/artex/e3contract"
)

// WriteEvent는 가짜 쓰기 기록이 받은 호출 하나다. Reserve면 Write.Outcome이 비어 있다.
type WriteEvent struct {
	Reserve bool
	Type    string
	// ObjectID는 Reserve 때 받은 요청 본문 칸 ID다(id_field). 없으면 ID 모름 예약이다.
	ObjectID string
	Write    e3contract.ConfirmationWrite
	// SentBefore는 이 호출 때까지 가짜 E²가 받은 생성 요청 수다(기록하고 나서 보내기 순서 확인용).
	SentBefore int
}

// Writes는 가짜 쓰기 기록(e3contract.WriteRecorder)이다. ReserveErr·SettleErr로 실패를 만든다.
type Writes struct {
	mu sync.Mutex
	// Transport가 있으면 호출 때 생성 요청 수를 남긴다.
	Transport *Transport
	// ReserveErr·SettleErr는 종류별로 돌려줄 오류다. 키가 ""이면 모든 종류다.
	ReserveErr map[string]error
	SettleErr  map[string]error
	events     []WriteEvent
}

func (w *Writes) creates() int {
	if w.Transport == nil {
		return 0
	}
	n := 0
	for _, c := range w.Transport.Calls() {
		if c.Key.Purpose == e3contract.PurposeCreate {
			n++
		}
	}
	return n
}

func pick(m map[string]error, typ string) error {
	if err, ok := m[typ]; ok {
		return err
	}
	return m[""]
}

func (w *Writes) Reserve(_ context.Context, _ e3contract.RunBinding, resourceType, objectID string) error {
	sent := w.creates()
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := pick(w.ReserveErr, resourceType); err != nil {
		return err
	}
	w.events = append(w.events, WriteEvent{Reserve: true, Type: resourceType, ObjectID: objectID, SentBefore: sent})
	return nil
}

func (w *Writes) Settle(_ context.Context, _ e3contract.RunBinding, write e3contract.ConfirmationWrite) error {
	sent := w.creates()
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := pick(w.SettleErr, write.ResourceType); err != nil {
		return err
	}
	w.events = append(w.events, WriteEvent{Type: write.ResourceType, Write: write, SentBefore: sent})
	return nil
}

// Events는 받은 호출(성공한 것만)의 사본이다.
func (w *Writes) Events() []WriteEvent {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]WriteEvent(nil), w.events...)
}
