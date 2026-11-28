package responseevidence

import (
	"errors"
	"regexp"
	"strings"
)

// ErrInvalid는 저장·전달된 근거가 이 형식의 범위를 벗어났다는 뜻이다. 손상 근거는 비교·모델 입력에 쓰지 않는다.
var ErrInvalid = errors.New("responseevidence: invalid evidence")

// 저장 근거의 최대 크기다. 추출기 상한보다 넉넉하지만 무제한이 아니다.
const (
	maxStoredFields  = 256
	maxStoredForms   = 16
	maxStoredInputs  = 64
	maxStoredPathLen = 512
	maxStoredSignals = 16
	maxStoredLimits  = 16
)

var (
	segmentPattern  = regexp.MustCompile(`^(\{key\}|\{protected\}|[A-Za-z_$][A-Za-z0-9_$\-]{0,63})?(\[\])*$`)
	digestPattern   = regexp.MustCompile(`^[0-9a-f]{16}$`)
	actionPattern   = regexp.MustCompile(`^(absolute:)?(same_path|/?(\{segment\}|[A-Za-z][A-Za-z0-9_-]{0,30})(/(\{segment\}|[A-Za-z][A-Za-z0-9_-]{0,30}))*|/)$`)
	inputNameStored = regexp.MustCompile(`^(|\{name\}|\{protected\}|[A-Za-z_][A-Za-z0-9_.\-\[\]]{0,63})$`)
)

var (
	knownMedia   = map[string]bool{"none": true, "html": true, "json": true, "plain_text": true, "xml": true, "binary": true, "other": true, "unknown": true}
	knownSignals = map[string]bool{SignalStackTrace: true, SignalStackTraceField: true, SignalSQLError: true, SignalDebugPage: true,
		SignalServerPath: true, SignalServerError: true, SignalExceptionName: true}
	knownLimits = map[string]bool{LimitBodyBytes: true, LimitDepth: true, LimitFields: true, LimitArrayItems: true, LimitStringBytes: true,
		LimitHTMLTokens: true, LimitForms: true, LimitInputs: true, LimitEncoded: true, LimitDuplicate: true}
	knownJSONTypes = map[string]bool{"object": true, "array": true, "string": true, "number": true, "bool": true, "null": true, "mixed": true}
	knownShapes    = map[string]bool{"": true, "empty": true, "numeric": true, "uuid": true, "hex": true, "datetime": true, "email": true,
		"url": true, "jwt": true, "text": true, "mixed": true}
	knownMethods = map[string]bool{"GET": true, "POST": true, "DIALOG": true, "OTHER": true}
)

// Validate는 근거의 형식·범위를 확인한다. 출처 결속은 호출자(저장 관찰 검증)가 따로 확인한다.
func Validate(ev Evidence) error {
	if ev.Schema != Schema || !ev.State.Valid() || !knownMedia[ev.MediaClass] || ev.BodyBytes < 0 || ev.StatusCode < 0 || ev.StatusCode > 599 ||
		len(ev.Signals) > maxStoredSignals || len(ev.Limits) > maxStoredLimits {
		return ErrInvalid
	}
	if !sortedUnique(ev.Signals, knownSignals) || !sortedUnique(ev.Limits, knownLimits) {
		return ErrInvalid
	}
	structures := 0
	for _, present := range []bool{ev.JSON != nil, ev.HTML != nil, ev.Text != nil} {
		if present {
			structures++
		}
	}
	if structures > 1 || structures == 1 && !ev.State.Structured() {
		return ErrInvalid
	}
	if ev.State == StateNotReceived && (ev.StatusCode != 0 || ev.MediaClass != "none" || ev.BodyBytes != 0) {
		return ErrInvalid
	}
	if ev.Headers != nil && (ev.State == StateNotReceived || ev.State == StateNotRecorded || !validateHeaders(*ev.Headers)) {
		return ErrInvalid
	}
	if ev.State == StateLimited && len(ev.Limits) == 0 || ev.State == StateComplete && len(ev.Limits) != 0 {
		return ErrInvalid
	}
	if ev.JSON != nil && (ev.MediaClass != "json" || validateJSON(*ev.JSON) != nil) ||
		ev.HTML != nil && (ev.MediaClass != "html" || validateHTML(*ev.HTML) != nil) ||
		ev.Text != nil && (ev.MediaClass != "plain_text" || ev.Text.Lines < 0 || ev.Text.NonEmptyLines < 0 || ev.Text.NonEmptyLines > ev.Text.Lines) {
		return ErrInvalid
	}
	return nil
}

func sortedUnique(values []string, known map[string]bool) bool {
	for i, v := range values {
		if !known[v] || i > 0 && values[i-1] >= v {
			return false
		}
	}
	return true
}

func validatePath(path string) bool {
	if path == "" || len(path) > maxStoredPathLen {
		return false
	}
	for i, part := range strings.Split(path, ".") {
		if part == "" || !segmentPattern.MatchString(part) || i > 0 && strings.HasPrefix(part, "[") {
			return false
		}
	}
	return true
}

func validateJSON(js JSONStructure) error {
	if !knownJSONTypes[js.RootType] || js.RootType == "mixed" || js.RootLength < 0 || js.FieldsOmitted < 0 || len(js.Fields) > maxStoredFields || js.Fields == nil {
		return ErrInvalid
	}
	for i, f := range js.Fields {
		if !validatePath(f.Path) || i > 0 && js.Fields[i-1].Path >= f.Path || !knownJSONTypes[f.Type] || !knownShapes[f.Shape] ||
			f.Count < 1 || f.Length < 0 || f.ValueDigest != "" && !digestPattern.MatchString(f.ValueDigest) ||
			f.Bool != nil && f.Type != "bool" || f.Protected && (f.Shape != "" || f.ValueDigest != "") ||
			f.ValueDigest != "" && f.Count != 1 {
			return ErrInvalid
		}
	}
	return nil
}

func validateHTML(h HTMLStructure) error {
	if h.Elements < 0 || h.AlertRegions < 0 || h.FormsOmitted < 0 || len(h.Forms) > maxStoredForms || !digestPattern.MatchString(h.StructureDigest) {
		return ErrInvalid
	}
	for tag, n := range h.TagCounts {
		if !countedTags[tag] || n < 1 {
			return ErrInvalid
		}
	}
	for _, f := range h.Forms {
		if !knownMethods[f.Method] || f.InputsOmitted < 0 || len(f.Inputs) > maxStoredInputs ||
			!(f.Action == "same_document" || f.Action == "invalid" || f.Action == "non_http" || f.Action == "{protected}" || actionPattern.MatchString(f.Action)) {
			return ErrInvalid
		}
		for _, in := range f.Inputs {
			if !inputNameStored.MatchString(in.Name) || !(inputTypes[in.Type] || in.Type == "other" || in.Type == "select" || in.Type == "textarea") {
				return ErrInvalid
			}
		}
	}
	return nil
}
