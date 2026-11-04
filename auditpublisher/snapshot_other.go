//go:build !linux

package auditpublisher

import (
	"context"
	"os"
)

type journalRoot struct{}
type journalSnapshot struct{ root *journalRoot }

func openJournalRoot(string, *os.File) (*journalRoot, error)        { return nil, ErrUnavailable }
func (*journalRoot) attach(string, int64) (*journalSnapshot, error) { return nil, ErrUnavailable }
func (*journalRoot) close() error                                   { return nil }
func (*journalSnapshot) read(context.Context) ([]byte, error)       { return nil, ErrUnavailable }
func (*journalSnapshot) current() error                             { return ErrUnavailable }
func (*journalSnapshot) close() error                               { return nil }
