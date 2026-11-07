//go:build !linux

package e2export

import "os"

type exportDirectory struct{ root *os.Root }

func openExportDirectory(string, bool) (*exportDirectory, error) { return nil, ErrConfig }
func (*exportDirectory) same(*exportDirectory) bool              { return false }
func (*exportDirectory) close()                                  {}
func (*exportDirectory) read(string, int64) ([]byte, error)      { return nil, ErrPublish }
func (*exportDirectory) write(string, []byte) error              { return ErrPublish }
