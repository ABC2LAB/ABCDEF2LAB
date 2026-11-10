//go:build !linux

package diagnosisprep

import "encoding/json"

type ProtectedJournal struct{}

func OpenProtectedJournal(string) (*ProtectedJournal, []json.RawMessage, error) {
	return nil, nil, ErrUnavailable
}
func (*ProtectedJournal) Check() error                 { return ErrUnavailable }
func (*ProtectedJournal) Append(json.RawMessage) error { return ErrUnavailable }
func (*ProtectedJournal) Close() error                 { return nil }
