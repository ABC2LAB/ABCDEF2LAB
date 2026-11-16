//go:build !linux

package exploreboundary

// OpenEvidence는 리눅스 밖에서 지원하지 않는다. 비공개 디렉터리 검사 없이 원자료를 쓰지 않는다.
func OpenEvidence(string, int64, []byte) (*Evidence, error) { return nil, ErrConfig }
