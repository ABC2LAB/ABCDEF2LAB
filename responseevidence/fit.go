package responseevidence

import (
	"encoding/json"
	"slices"
	"sort"
	"strings"
)

// Fit은 근거를 직렬화 maxBytes 안으로 줄인다. 정해진 순서로만 줄이고(태그 수 → 깊은 JSON 경로 → 폼 입력 → 폼 → 구조 전체),
// 줄일 때마다 encoded_size 상한 표시를 붙인다. 구조를 모두 뺐으면 구조 비교는 할 수 없다(Compare가 body_structure를 비교 못 함으로 남김).
// 줄여도 넘으면 false다.
func Fit(ev Evidence, maxBytes int) (Evidence, bool) {
	if fits(ev, maxBytes) {
		return Clone(ev), true
	}
	ev = Clone(ev)
	mark := func() {
		if ev.State == StateComplete {
			ev.State = StateLimited
		}
		for _, l := range ev.Limits {
			if l == LimitEncoded {
				return
			}
		}
		ev.Limits = append(ev.Limits, LimitEncoded)
		sort.Strings(ev.Limits)
	}
	if ev.HTML != nil && ev.HTML.TagCounts != nil {
		ev.HTML.TagCounts = nil
		mark()
		if fits(ev, maxBytes) {
			return ev, true
		}
	}
	if ev.JSON != nil {
		// 얕은 경로부터 남긴다. 같은 깊이는 경로 순서다.
		fields := append([]JSONField(nil), ev.JSON.Fields...)
		sort.SliceStable(fields, func(i, j int) bool {
			di, dj := pathDepth(fields[i].Path), pathDepth(fields[j].Path)
			if di != dj {
				return di < dj
			}
			return fields[i].Path < fields[j].Path
		})
		for len(fields) > 0 && !fits(ev, maxBytes) {
			keep := len(fields) / 2
			ev.JSON.FieldsOmitted += len(fields) - keep
			fields = fields[:keep]
			kept := append([]JSONField(nil), fields...)
			sort.Slice(kept, func(i, j int) bool { return kept[i].Path < kept[j].Path })
			ev.JSON.Fields = kept
			if ev.JSON.Fields == nil {
				ev.JSON.Fields = []JSONField{}
			}
			mark()
		}
		if fits(ev, maxBytes) {
			return ev, true
		}
	}
	if ev.HTML != nil {
		for i := range ev.HTML.Forms {
			if len(ev.HTML.Forms[i].Inputs) != 0 {
				ev.HTML.Forms[i].InputsOmitted += len(ev.HTML.Forms[i].Inputs)
				ev.HTML.Forms[i].Inputs = nil
				mark()
			}
		}
		if fits(ev, maxBytes) {
			return ev, true
		}
		if len(ev.HTML.Forms) != 0 {
			ev.HTML.FormsOmitted += len(ev.HTML.Forms)
			ev.HTML.Forms = nil
			mark()
			if fits(ev, maxBytes) {
				return ev, true
			}
		}
	}
	if ev.JSON != nil || ev.HTML != nil || ev.Text != nil {
		ev.JSON, ev.HTML, ev.Text = nil, nil, nil
		mark()
	}
	return ev, fits(ev, maxBytes)
}

func fits(ev Evidence, maxBytes int) bool {
	raw, err := json.Marshal(ev)
	return err == nil && len(raw) <= maxBytes
}

func pathDepth(path string) int {
	return strings.Count(path, ".") + strings.Count(path, "[]")
}

// Clone은 근거의 중첩 슬라이스·맵·포인터까지 복사한다. 반환된 실행 결과를 바꾸어도 보관 중인 근거는 바뀌지 않는다.
func Clone(ev Evidence) Evidence {
	out := ev
	out.Limits = slices.Clone(ev.Limits)
	out.Signals = slices.Clone(ev.Signals)
	if ev.JSON != nil {
		js := *ev.JSON
		js.Fields = slices.Clone(ev.JSON.Fields)
		out.JSON = &js
	}
	if ev.HTML != nil {
		h := *ev.HTML
		if ev.HTML.TagCounts != nil {
			h.TagCounts = map[string]int{}
			for k, v := range ev.HTML.TagCounts {
				h.TagCounts[k] = v
			}
		}
		h.Forms = slices.Clone(ev.HTML.Forms)
		for i := range h.Forms {
			h.Forms[i].Inputs = slices.Clone(ev.HTML.Forms[i].Inputs)
		}
		out.HTML = &h
	}
	if ev.Text != nil {
		t := *ev.Text
		out.Text = &t
	}
	if ev.Headers != nil {
		h := *ev.Headers
		h.Present = slices.Clone(ev.Headers.Present)
		h.Repeated = slices.Clone(ev.Headers.Repeated)
		if ev.Headers.Cookies != nil {
			cookies := *ev.Headers.Cookies
			h.Cookies = &cookies
		}
		out.Headers = &h
	}
	return out
}
