package main

import (
	"github.com/Autumn-27/artex/diagnosisprep"
	"reflect"
)

// ReadRunApprovals는 저장 Job의 대상 권한 상태만 한 잠금 아래 읽는다. binding 생성·인증 준비·DNS·송신은 하지 않는다.
// 일치해도 실행의 모든 현재 조건을 재검증한 것은 아니므로 not_revalidated다. 조회로 재개 권위를 만들지 않는다.
func (b *urlPreparationRuntime) ReadRunApprovals(principal string, jobs []diagnosisprep.Job) map[string]string {
	out := make(map[string]string, len(jobs))
	if b.product == nil {
		return out
	}
	registry := b.product.targets
	registry.mu.Lock()
	defer registry.mu.Unlock()
	trusted := principal == "ARTEX" && registry.check() == nil
	now := registry.now()
	for _, job := range jobs {
		status := "not_revalidated"
		switch {
		case !trusted:
			status = "unavailable"
		case job.Scope == nil:
			status = "not_revalidated"
		default:
			rec, exists := registry.records[job.Scope.TargetRef]
			switch {
			case !exists || rec.Principal != principal:
				status = "unavailable"
			case rec.Target.State == "revoked":
				status = "revoked"
			case !now.Before(rec.Target.ExpiresAt):
				status = "expired"
			case rec.Target.State != "approved" || job.Scope.ScopeRef != "scope-"+rec.Target.ID || !reflect.DeepEqual(job.Scope.PathPrefixes, rec.Target.PathPrefixes) || job.Scope.Budget.Requests != rec.Target.Requests || job.Scope.ExpiresAt.After(rec.Target.ExpiresAt):
				status = "changed"
			default:
				for _, method := range job.Scope.Methods {
					found := false
					for _, allowed := range rec.Target.Methods {
						found = found || method == allowed
					}
					if !found {
						status = "changed"
						break
					}
				}
			}
		}
		out[job.ID] = status
	}
	return out
}
