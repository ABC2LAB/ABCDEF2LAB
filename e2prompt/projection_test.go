package e2prompt

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestFinalizePreservesEveryComparedField(t *testing.T) {
	base := Identity{Version: Version, Key: "e2_worker", Source: "builtin", TemplateDigest: strings.Repeat("a", 64), GuidanceDigest: strings.Repeat("b", 64), ControlDigest: strings.Repeat("c", 64), ToolsDigest: strings.Repeat("d", 64)}
	for name, change := range map[string]func(*Identity){"source": func(i *Identity) { i.Source = "override" }, "template": func(i *Identity) { i.TemplateDigest = strings.Repeat("e", 64) }, "guidance": func(i *Identity) { i.GuidanceDigest = strings.Repeat("e", 64) }, "control": func(i *Identity) { i.ControlDigest = strings.Repeat("e", 64) }, "tools": func(i *Identity) { i.ToolsDigest = strings.Repeat("e", 64) }} {
		t.Run(name, func(t *testing.T) {
			other := base
			change(&other)
			p := Projection{ExpectedCount: 2, ObservedCount: 2, Observations: []Observation{{Identity: base}, {Identity: other}}}
			p.Finalize(true, false)
			if p.State != "conflict" || p.Complete || len(p.Observations) != 2 {
				t.Fatal("conflict was hidden")
			}
		})
	}
}

func TestParseStrictDetail(t *testing.T) {
	valid := `{"version":"e2-worker-prompt/v1","key":"e2_worker","source":"builtin","template_digest":"` + strings.Repeat("a", 64) + `","guidance_digest":"` + strings.Repeat("b", 64) + `","control_digest":"` + strings.Repeat("c", 64) + `","tools_digest":"` + strings.Repeat("d", 64) + `","system_digest":"` + strings.Repeat("e", 64) + `"}`
	if _, err := Parse([]byte(valid)); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(map[string]any){"version": func(m map[string]any) { m["version"] = "v2" }, "key": func(m map[string]any) { m["key"] = "worker" }, "source": func(m map[string]any) { m["source"] = "db" }, "missing": func(m map[string]any) { delete(m, "tools_digest") }, "null": func(m map[string]any) { m["tools_digest"] = nil }, "type": func(m map[string]any) { m["tools_digest"] = 1 }, "upper": func(m map[string]any) { m["tools_digest"] = strings.Repeat("A", 64) }, "unknown": func(m map[string]any) { m["private"] = "raw" }} {
		t.Run(name, func(t *testing.T) {
			var m map[string]any
			json.Unmarshal([]byte(valid), &m)
			mutate(m)
			raw, _ := json.Marshal(m)
			if _, err := Parse(raw); err == nil {
				t.Fatal("invalid accepted")
			}
		})
	}
	for _, raw := range [][]byte{[]byte(strings.Replace(valid, `"source":`, `"source":"override","source":`, 1)), []byte(valid + ` {}`), append([]byte(valid), 0xff)} {
		if _, err := Parse(raw); err == nil {
			t.Fatal("invalid JSON accepted")
		}
	}
}
