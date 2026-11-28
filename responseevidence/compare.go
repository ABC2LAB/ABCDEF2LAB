package responseevidence

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// ComparisonSchema는 두 응답 근거의 비교 결과 형식이다.
const ComparisonSchema = "jindo.candidate-response-comparison/v1"

// Role은 비교에 넣은 관찰의 역할이다. 정상 대조는 재현이 아니다.
type Role string

const (
	RoleOriginal      Role = "original"
	RoleReproduction  Role = "reproduction"
	RoleNormalControl Role = "normal_control"
)

// Relation은 응답 구조 비교의 사실 판단이다. 취약점의 의미 판단과 다르다.
type Relation string

const (
	RelationEquivalent           Relation = "equivalent"
	RelationEquivalentNormalized Relation = "equivalent_after_normalization"
	RelationDifferent            Relation = "different"
	// RelationIncomparable은 한쪽 이상의 근거가 없거나 불완전해 비교하지 않았다는 뜻이다. 차이가 없다는 뜻이 아니다.
	RelationIncomparable Relation = "incomparable"
)

// 차이 종류다.
const (
	DiffStatus      = "status_code"
	DiffMedia       = "media_class"
	DiffEmpty       = "empty_body"
	DiffSignal      = "error_signal"
	DiffRootType    = "json_root_type"
	DiffRootLength  = "json_root_length"
	DiffErrorShape  = "json_error_shape"
	DiffPresence    = "field_presence"
	DiffType        = "field_type"
	DiffLength      = "field_length"
	DiffEmptyValue  = "field_empty"
	DiffBool        = "field_bool"
	DiffValue       = "field_value"
	DiffShape       = "field_shape"
	DiffLoginForm   = "password_input"
	DiffForms       = "form_count"
	DiffForm        = "form"
	DiffAlert       = "alert_region"
	DiffTitle       = "html_title"
	DiffStructure   = "html_structure"
	DiffTextLines   = "text_lines"
	DiffHeader      = "header_presence"
	DiffHeaderValue = "header_attribute"
	maxDifferences  = 32
	maxNormalized   = 16
	maxNotCompareds = 16
)

type Difference struct {
	Kind  string `json:"kind"`
	Path  string `json:"path,omitempty"`
	Left  string `json:"left"`
	Right string `json:"right"`
}

// Comparison은 두 응답 근거 비교 결과다. 정규화로 넘긴 항목과 비교하지 못한 항목을 함께 남긴다.
type Comparison struct {
	Schema             string       `json:"schema"`
	Left               Role         `json:"left"`
	Right              Role         `json:"right"`
	Relation           Relation     `json:"relation"`
	Incomparable       []string     `json:"incomparable,omitempty"`
	Differences        []Difference `json:"differences,omitempty"`
	DifferencesOmitted int          `json:"differences_omitted,omitempty"`
	// Normalized는 요청마다 바뀌는 정상 값으로 보고 차이에서 뺀 항목이다(경로:기준). 존재·자료형 차이는 빼지 않는다.
	Normalized []string `json:"normalized,omitempty"`
	// NotCompared는 상한·digest 부재로 비교하지 못한 항목이다.
	NotCompared []string `json:"not_compared,omitempty"`
	// Similarity는 구조 유사도(0~100)다. 계산하지 않았으면 생략한다.
	Similarity *int   `json:"similarity,omitempty"`
	BodyBytes  [2]int `json:"body_bytes"`
}

// Compare는 두 근거를 비교한다. 같은 입력은 같은 출력을 낸다.
func Compare(left, right Evidence, leftRole, rightRole Role) Comparison {
	c := &comparer{out: Comparison{Schema: ComparisonSchema, Left: leftRole, Right: rightRole, BodyBytes: [2]int{left.BodyBytes, right.BodyBytes}}}
	for side, ev := range map[string]Evidence{"left": left, "right": right} {
		if Validate(ev) != nil {
			c.out.Incomparable = append(c.out.Incomparable, side+"_evidence_invalid")
			continue
		}
		switch ev.State {
		case StateComplete, StateLimited, StateUnsupportedMedia:
		default:
			c.out.Incomparable = append(c.out.Incomparable, side+"_"+string(ev.State))
		}
	}
	if len(c.out.Incomparable) != 0 {
		sort.Strings(c.out.Incomparable)
		c.out.Relation = RelationIncomparable
		return c.out
	}
	if left.StatusCode != right.StatusCode {
		c.diff(DiffStatus, "", strconv.Itoa(left.StatusCode), strconv.Itoa(right.StatusCode))
	}
	if left.MediaClass != right.MediaClass {
		c.diff(DiffMedia, "", left.MediaClass, right.MediaClass)
	}
	if left.Empty != right.Empty {
		c.diff(DiffEmpty, "", strconv.FormatBool(left.Empty), strconv.FormatBool(right.Empty))
	}
	c.signals(left, right)
	c.headers(left.Headers, right.Headers)
	switch {
	case left.State == StateUnsupportedMedia || right.State == StateUnsupportedMedia:
		c.notCompared("body_structure")
	case left.MediaClass != right.MediaClass:
		c.notCompared("body_structure")
	case left.Empty && right.Empty:
	case left.JSON != nil && right.JSON != nil:
		c.json(*left.JSON, *right.JSON)
	case left.HTML != nil && right.HTML != nil:
		c.html(*left.HTML, *right.HTML)
	case left.Text != nil && right.Text != nil:
		if left.Text.NonEmptyLines != right.Text.NonEmptyLines {
			c.diff(DiffTextLines, "", strconv.Itoa(left.Text.NonEmptyLines), strconv.Itoa(right.Text.NonEmptyLines))
		}
	default:
		// 한쪽이라도 구조를 담지 못했다(본문 해석 상한 등). 차이가 없다고 하지 않는다.
		c.notCompared("body_structure")
	}
	for _, ev := range []Evidence{left, right} {
		for _, code := range ev.Limits {
			c.notCompared("limit:" + code)
		}
	}
	return c.finish()
}

type comparer struct{ out Comparison }

func (c *comparer) diff(kind, path, left, right string) {
	if len(c.out.Differences) >= maxDifferences {
		c.out.DifferencesOmitted++
		return
	}
	c.out.Differences = append(c.out.Differences, Difference{Kind: kind, Path: path, Left: left, Right: right})
}

func (c *comparer) notCompared(code string) {
	for _, have := range c.out.NotCompared {
		if have == code {
			return
		}
	}
	if len(c.out.NotCompared) < maxNotCompareds {
		c.out.NotCompared = append(c.out.NotCompared, code)
	}
}

func (c *comparer) normalized(path, basis string) {
	if len(c.out.Normalized) < maxNormalized {
		c.out.Normalized = append(c.out.Normalized, path+":"+basis)
	} else {
		c.notCompared("normalized_overflow")
	}
}

func (c *comparer) finish() Comparison {
	sort.Strings(c.out.NotCompared)
	switch {
	case len(c.out.Differences) != 0:
		c.out.Relation = RelationDifferent
	case len(c.out.Normalized) != 0:
		c.out.Relation = RelationEquivalentNormalized
	default:
		c.out.Relation = RelationEquivalent
	}
	return c.out
}

func (c *comparer) signals(left, right Evidence) {
	l, r := map[string]bool{}, map[string]bool{}
	for _, s := range left.Signals {
		l[s] = true
	}
	for _, s := range right.Signals {
		r[s] = true
	}
	codes := map[string]bool{}
	for s := range l {
		codes[s] = true
	}
	for s := range r {
		codes[s] = true
	}
	sorted := make([]string, 0, len(codes))
	for s := range codes {
		sorted = append(sorted, s)
	}
	sort.Strings(sorted)
	for _, s := range sorted {
		if l[s] != r[s] {
			c.diff(DiffSignal, s, presence(l[s]), presence(r[s]))
		}
	}
}

func presence(b bool) string {
	if b {
		return "present"
	}
	return "absent"
}

func (c *comparer) json(l, r JSONStructure) {
	if l.RootType != r.RootType {
		c.diff(DiffRootType, "", l.RootType, r.RootType)
		c.notCompared("json_fields")
		zero := 0
		c.out.Similarity = &zero
		return
	}
	// 객체 키 수 차이는 아래 필드 존재 차이로 드러난다. 배열 길이만 따로 비교한다.
	if l.RootType == "array" && l.RootLength != r.RootLength {
		c.diff(DiffRootLength, "", strconv.Itoa(l.RootLength), strconv.Itoa(r.RootLength))
	}
	if l.ErrorShape != r.ErrorShape {
		c.diff(DiffErrorShape, "", presence(l.ErrorShape), presence(r.ErrorShape))
	}
	if l.DuplicateKeys || r.DuplicateKeys {
		c.notCompared("json_duplicate_keys")
	}
	lf, rf := map[string]JSONField{}, map[string]JSONField{}
	for _, f := range l.Fields {
		lf[f.Path] = f
	}
	for _, f := range r.Fields {
		rf[f.Path] = f
	}
	paths := map[string]bool{}
	for p := range lf {
		paths[p] = true
	}
	for p := range rf {
		paths[p] = true
	}
	sorted := make([]string, 0, len(paths))
	for p := range paths {
		sorted = append(sorted, p)
	}
	sort.Strings(sorted)
	same := 0
	for _, path := range sorted {
		a, okA := lf[path]
		b, okB := rf[path]
		switch {
		case okA && !okB:
			if r.FieldsOmitted > 0 {
				// 상대가 필드 상한으로 일부만 담았으면 부재를 주장하지 않는다.
				c.notCompared("right_fields_omitted")
				continue
			}
			c.diff(DiffPresence, path, "present", "absent")
			continue
		case !okA && okB:
			if l.FieldsOmitted > 0 {
				c.notCompared("left_fields_omitted")
				continue
			}
			c.diff(DiffPresence, path, "absent", "present")
			continue
		}
		if a.Type != b.Type {
			c.diff(DiffType, path, a.Type, b.Type)
			continue
		}
		same++
		if a.Length != b.Length && a.Type == "array" {
			c.diff(DiffLength, path, strconv.Itoa(a.Length), strconv.Itoa(b.Length))
		}
		if a.Empty != b.Empty {
			c.diff(DiffEmptyValue, path, strconv.FormatBool(a.Empty), strconv.FormatBool(b.Empty))
		}
		if a.Protected || b.Protected {
			c.notCompared("protected_value")
			continue
		}
		if (a.Type == "string" || a.Type == "number" || a.Type == "bool") &&
			(credentialPath(path) || a.Shape == "jwt" || b.Shape == "jwt") {
			// 자격·세션 스칼라는 값 digest와 bool 값을 남기지 않는다.
			c.notCompared("credential_value")
			continue
		}
		if a.Bool != nil && b.Bool != nil && *a.Bool != *b.Bool {
			c.diff(DiffBool, path, strconv.FormatBool(*a.Bool), strconv.FormatBool(*b.Bool))
			continue
		}
		if a.Type == "string" && a.Shape != b.Shape {
			c.diff(DiffShape, path, a.Shape, b.Shape)
			continue
		}
		switch {
		case a.ValueDigest != "" && b.ValueDigest != "" && a.ValueDigest != b.ValueDigest:
			if basis := volatileBasis(path, a.Shape); basis != "" {
				c.normalized(path, basis)
			} else {
				c.diff(DiffValue, path, "value_a", "value_b")
			}
		case (a.ValueDigest == "") != (b.ValueDigest == "") && (a.Type == "string" || a.Type == "number" || a.Type == "bool"):
			c.notCompared("value_digest_unavailable")
		}
	}
	if len(sorted) != 0 {
		sim := same * 100 / len(sorted)
		c.out.Similarity = &sim
	}
	if l.FieldsOmitted > 0 || r.FieldsOmitted > 0 {
		c.notCompared("json_fields_omitted")
	}
}

// volatileBasis는 요청마다 정상적으로 바뀌는 값으로 볼 근거를 돌려준다. 이름이나 모양이 맞을 때만이고, ID는 넣지 않는다
// (다른 계정·다른 자원이 돌아온 것은 보안상 중요한 차이일 수 있다).
func volatileBasis(path, shape string) string {
	name := path
	if i := strings.LastIndex(name, "."); i >= 0 {
		name = name[i+1:]
	}
	name = strings.ToLower(strings.TrimSuffix(name, "[]"))
	for _, word := range []string{"csrf", "xsrf", "nonce", "token", "timestamp", "time", "date", "created", "updated",
		"modified", "expires", "expiry", "issued", "trace", "span", "etag", "request_id", "requestid", "correlation"} {
		if strings.Contains(name, word) {
			return "volatile_name"
		}
	}
	switch name {
	case "exp", "iat", "nbf", "ts":
		return "volatile_name"
	}
	if shape == "datetime" || shape == "jwt" {
		return "volatile_shape"
	}
	return ""
}

func (c *comparer) html(l, r HTMLStructure) {
	if l.PasswordInput != r.PasswordInput {
		c.diff(DiffLoginForm, "", presence(l.PasswordInput), presence(r.PasswordInput))
	}
	if (l.AlertRegions > 0) != (r.AlertRegions > 0) {
		c.diff(DiffAlert, "", presence(l.AlertRegions > 0), presence(r.AlertRegions > 0))
	}
	if l.Title != r.Title {
		c.diff(DiffTitle, "", presence(l.Title), presence(r.Title))
	}
	lc, rc := len(l.Forms)+l.FormsOmitted, len(r.Forms)+r.FormsOmitted
	if lc != rc {
		c.diff(DiffForms, "", strconv.Itoa(lc), strconv.Itoa(rc))
	}
	for i := 0; i < len(l.Forms) && i < len(r.Forms); i++ {
		a, b := l.Forms[i], r.Forms[i]
		path := fmt.Sprintf("form[%d]", i)
		if a.InputsOmitted > 0 || b.InputsOmitted > 0 {
			c.notCompared("html_inputs_omitted")
		}
		if a.Method != b.Method {
			c.diff(DiffForm, path+".method", a.Method, b.Method)
		}
		if a.Action != b.Action {
			c.diff(DiffForm, path+".action", a.Action, b.Action)
		}
		if inputSignature(a) != inputSignature(b) {
			c.diff(DiffForm, path+".inputs", inputSignature(a), inputSignature(b))
		}
	}
	if l.FormsOmitted > 0 || r.FormsOmitted > 0 {
		c.notCompared("html_forms_omitted")
	}
	sim := 100
	similarityKnown := l.TagCounts != nil && r.TagCounts != nil
	if !similarityKnown {
		c.notCompared("html_tag_counts")
	}
	if l.StructureDigest != r.StructureDigest {
		if !similarityKnown {
			// Fit이 태그 수를 버렸으면 구조 유사도를 알 수 없다.
			c.diff(DiffStructure, "", l.StructureDigest, r.StructureDigest)
		} else {
			sim = tagSimilarity(l.TagCounts, r.TagCounts)
			if sim == 100 {
				// 같은 고정 태그 수라도 목록 순서 외에 세지 않은 태그나 중첩이 바뀌었을 수 있다.
				sim = 99
			}
			c.diff(DiffStructure, "", l.StructureDigest, r.StructureDigest)
		}
	}
	if similarityKnown {
		c.out.Similarity = &sim
	}
}

func inputSignature(f HTMLForm) string {
	parts := make([]string, 0, len(f.Inputs))
	for _, in := range f.Inputs {
		parts = append(parts, in.Name+":"+in.Type)
	}
	sort.Strings(parts)
	s := strings.Join(parts, ",")
	if f.InputsOmitted > 0 {
		s += ",+" + strconv.Itoa(f.InputsOmitted)
	}
	return s
}

func itoa(n int) string { return strconv.Itoa(n) }

func tagSimilarity(l, r map[string]int) int {
	min, max := 0, 0
	for tag := range countedTags {
		a, b := l[tag], r[tag]
		if a < b {
			min, max = min+a, max+b
		} else {
			min, max = min+b, max+a
		}
	}
	if max == 0 {
		return 100
	}
	return min * 100 / max
}
