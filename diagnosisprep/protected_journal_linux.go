//go:build linux

package diagnosisprep

import (
	"encoding/json"
	"sync"
)

// ProtectedJournal reuses the coordinator's descriptor-bound, fsynced chain/head
// storage. Callers use a separate private directory; no journal path is an RPC input.
type ProtectedJournal struct {
	mu      sync.Mutex
	journal *journal
}

func OpenProtectedJournal(path string) (*ProtectedJournal, []json.RawMessage, error) {
	j, records, e := openJournal(path)
	if e != nil {
		return nil, nil, e
	}
	return &ProtectedJournal{journal: j}, records, nil
}
func (p *ProtectedJournal) Check() error { p.mu.Lock(); defer p.mu.Unlock(); return p.check() }
func (p *ProtectedJournal) check() error {
	j := p.journal
	if j == nil || j.poisoned || !j.root.current() || !j.root.matches(journalName, j.file, j.stat) || !j.root.matches(headName, j.head, j.headStat) {
		return ErrUnavailable
	}
	return nil
}
func (p *ProtectedJournal) Append(raw json.RawMessage) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.check() != nil {
		return ErrUnavailable
	}
	return p.journal.append(raw)
}
func (p *ProtectedJournal) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.journal == nil {
		return nil
	}
	e := p.journal.close()
	p.journal = nil
	return e
}
