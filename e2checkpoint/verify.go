package e2checkpoint

import (
	"bytes"
	"context"
	"crypto/sha256"
	"io"

	"github.com/Autumn-27/artex/e2proxy"
)

// VerifyCurrent는 인증된 현재 epoch/head를 읽고 독립 저널 스냅샷의
// MAC·체인·끝 지점과 저장 당시의 정확한 바이트 digest를 확인한다.
// 현재성은 Host가 lease 동안 epoch·권한 교체를 fence한다는 전제에 선다.
func (s *Store) VerifyCurrent(ctx context.Context, query Query, source io.Reader) (e2proxy.JournalSummary, Head, error) {
	if !validEpoch(query.Epoch) || !validBinding(query.Binding) || source == nil {
		return e2proxy.JournalSummary{}, Head{}, ErrRequest
	}
	ctx, lease, release, err := s.acquire(ctx, Access{Purpose: VerifyAccess, Epoch: query.Epoch, Binding: query.Binding})
	if err != nil {
		return e2proxy.JournalSummary{}, Head{}, err
	}
	defer release()
	if err := checkEpoch(ctx, s.db, query.Epoch, false); err != nil {
		return e2proxy.JournalSummary{}, Head{}, err
	}
	current, err := readHead(ctx, s.db, query.Epoch, query.Binding, false)
	if err != nil {
		return e2proxy.JournalSummary{}, Head{}, err
	}
	data, err := io.ReadAll(io.LimitReader(&contextReader{ctx: ctx, reader: source}, s.maxBytes+1))
	if err != nil || ctx.Err() != nil || int64(len(data)) > s.maxBytes {
		return e2proxy.JournalSummary{}, Head{}, ErrIntegrity
	}
	defer clear(data)
	if int64(len(data)) != current.snapshotBytes || sha256.Sum256(data) != current.snapshotDigest {
		return e2proxy.JournalSummary{}, Head{}, ErrIntegrity
	}
	key, err := lease.ResolveKey(ctx, query.Binding)
	if err != nil || len(key) < 32 || len(key) > 4096 || ctx.Err() != nil {
		return e2proxy.JournalSummary{}, Head{}, ErrAuthority
	}
	owned := bytes.Clone(key)
	summary, err := e2proxy.VerifyJournal(ctx, bytes.NewReader(data), owned, current.Checkpoint)
	clear(owned)
	if err != nil {
		return e2proxy.JournalSummary{}, Head{}, ErrIntegrity
	}
	keyRef, err := headerKeyRef(data)
	if err != nil || keyRef != query.Binding.KeyRef {
		return e2proxy.JournalSummary{}, Head{}, ErrIntegrity
	}
	if err := checkEpoch(ctx, s.db, query.Epoch, false); err != nil {
		return e2proxy.JournalSummary{}, Head{}, err
	}
	latest, err := readHead(ctx, s.db, query.Epoch, query.Binding, false)
	if err != nil || latest.Head != current.Head || latest.snapshotBytes != current.snapshotBytes ||
		latest.snapshotDigest != current.snapshotDigest || ctx.Err() != nil || lease.Context().Err() != nil {
		return e2proxy.JournalSummary{}, Head{}, ErrIntegrity
	}
	return summary, current.Head, nil
}
