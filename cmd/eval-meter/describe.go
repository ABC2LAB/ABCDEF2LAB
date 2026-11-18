package main

import (
	"encoding/json"
	"io"
	"reflect"
	"runtime/debug"
	"sort"
	"strings"
)

// describeFormat는 -role describe 출력 형식이다. 러너는 회차를 띄우기 전에(모델을 부르기 전에) 이 출력으로 계측기·
// 게이트웨이 이미지가 자기 코드가 요구하는 표식 칸과 같은 빌드인지 확인한다(2026-11-17 파일럿 B 1회차가 #638 이전
// 이미지로 돌아 모델 비용을 쓴 뒤 llm_marker_invalid로 무효가 됨).
const describeFormat = "abc2lab.eval-meter-describe/v1"

// sourceRevision은 빌드한 소스 커밋이다. git worktree(.git이 파일)에서는 Go가 VCS 정보를 넣지 않으므로 빌드 명령이
// -ldflags "-X main.sourceRevision=<커밋 40자>"로 명시한다(official-run.md). 비어 있으면 러너가 회차를 시작하지 않는다.
var sourceRevision string

type describedFields struct {
	Required []string `json:"required"`
	Optional []string `json:"optional"`
}

type describeBuild struct {
	SourceRevision string `json:"source_revision"`
	VCSRevision    string `json:"vcs_revision"`
	VCSModified    *bool  `json:"vcs_modified"`
	GoVersion      string `json:"go_version"`
}

type describeOutput struct {
	Format  string                     `json:"format"`
	Markers map[string]describedFields `json:"markers"`
	Build   describeBuild              `json:"build"`
}

// jsonFields는 구조체의 json 칸 이름이다. omitempty 칸은 빠질 수 있어 따로 둔다. 코드의 구조체에서 바로 뽑으므로
// 표식 칸이 바뀌면 이 출력도 같이 바뀐다.
func jsonFields(value any) describedFields {
	fields := describedFields{Required: []string{}, Optional: []string{}}
	kind := reflect.TypeOf(value)
	for i := 0; i < kind.NumField(); i++ {
		tag := kind.Field(i).Tag.Get("json")
		if tag == "" || tag == "-" {
			continue
		}
		name, options, _ := strings.Cut(tag, ",")
		if strings.Contains(","+options+",", ",omitempty,") {
			fields.Optional = append(fields.Optional, name)
		} else {
			fields.Required = append(fields.Required, name)
		}
	}
	sort.Strings(fields.Required)
	sort.Strings(fields.Optional)
	return fields
}

func buildFromInfo(info *debug.BuildInfo, ok bool) describeBuild {
	build := describeBuild{SourceRevision: sourceRevision}
	if !ok || info == nil {
		return build
	}
	build.GoVersion = info.GoVersion
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			build.VCSRevision = setting.Value
		case "vcs.modified":
			modified := setting.Value == "true"
			build.VCSModified = &modified
		}
	}
	return build
}

func runDescribe(stdout, stderr io.Writer) int {
	info, ok := debug.ReadBuildInfo()
	out := describeOutput{
		Format: describeFormat,
		Markers: map[string]describedFields{
			"llm-verified": jsonFields(verifiedMarker{}),
			"engine-row":   jsonFields(engineRow{}),
		},
		Build: buildFromInfo(info, ok),
	}
	if err := json.NewEncoder(stdout).Encode(out); err != nil {
		return fail(stderr, probeOutputFailed)
	}
	return 0
}
