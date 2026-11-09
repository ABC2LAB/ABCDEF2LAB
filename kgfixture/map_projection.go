package kgfixture

import "sort"

// MapResource는 지도 축에 필요한 참조만 담는다. 표식·정책 값은 복사하지 않는다.
type MapResource struct {
	ResourceRef string   `json:"resource_ref"`
	OwnerRef    string   `json:"owner_ref"`
	Principals  []string `json:"principals"`
}

// MapResources는 등록 시 동결할 비민감 projection이다. 만료된 실행 핸들을 다시 열지 않는다.
func (s *Snapshot) MapResources() ([]MapResource, error) {
	if s == nil || s.Err() != nil {
		return nil, reject(CodeSnapshotInvalid)
	}
	out := make([]MapResource, 0, len(s.resources))
	for _, ref := range s.ResourceRefs() {
		r := s.resources[ref]
		principals := make([]string, 0, len(r.policies))
		for principal := range r.policies {
			principals = append(principals, principal)
		}
		sort.Strings(principals)
		out = append(out, MapResource{ResourceRef: ref, OwnerRef: r.owner, Principals: principals})
	}
	if s.Err() != nil {
		return nil, reject(CodeSnapshotInvalid)
	}
	return out, nil
}
