// Package e2config은 평가 러너의 보호된 준비 파일을 검증한다.
package e2config

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Autumn-27/artex/internal/privateinput"
)

const Format = "abc2lab.e2-runner-config/v1"
const MaxConfigBytes = 64 << 10
const MaxPolicyBytes = 256 << 10

var ErrInvalid = errors.New("e2_runner_config_invalid")
var ref = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:/-]{0,127}$`)
var fileRef = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

type Evaluation struct {
	LLMBackend string `json:"llm_backend"`
	TargetKind string `json:"target_kind"`
}
type Config struct {
	Format         string      `json:"format"`
	EvalRunID      string      `json:"eval_run_id"`
	ReservationRef string      `json:"reservation_ref"`
	ModelTag       string      `json:"model_tag"`
	HeaderMode     string      `json:"header_mode"`
	Evaluation     *Evaluation `json:"evaluation"`
	PolicyFile     string      `json:"policy_file"`
	AuditKeyFile   string      `json:"audit_key_file"`
	Listen         string      `json:"listen"`
	Upstream       string      `json:"upstream"`
	EvidenceDir    string      `json:"evidence_dir"`
	ControlDir     string      `json:"control_dir"`
	ConfigSHA256   string      `json:"-"`
	PolicySHA256   string      `json:"-"`
	PolicyPath     string      `json:"-"`
	AuditKeyPath   string      `json:"-"`
	path           string
	files          []pinned
	dirs           []pinned
	original       snapshot
}
type snapshot struct {
	wire                                      []byte
	configSHA, policySHA, policyPath, keyPath string
}

type pinned struct {
	path   string
	info   os.FileInfo
	digest string
	limit  int64
}

func (Config) String() string   { return "e2config.Config[protected]" }
func (Config) GoString() string { return "e2config.Config[protected]" }
func digest(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }
func validRef(s string) bool    { return ref.MatchString(s) && !strings.Contains(s, "://") }

// StrictJSON은 중복 키, 잘못된 UTF-8, 알 수 없는 필드와 null을 거부한다.
// 평가 선언만 null을 허용한다. 오류에는 입력 내용을 담지 않는다.
func StrictJSON(data []byte, target any) error {
	if !utf8.Valid(data) {
		return ErrInvalid
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	if scan(d, "", 0, false) != nil {
		return ErrInvalid
	}
	if _, err := d.Token(); err != io.EOF {
		return ErrInvalid
	}
	d = json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if d.Decode(target) != nil {
		return ErrInvalid
	}
	return nil
}
func scan(d *json.Decoder, key string, depth int, allowNull bool) error {
	if depth > 64 {
		return ErrInvalid
	}
	t, err := d.Token()
	if err != nil {
		return ErrInvalid
	}
	if t == nil && key != "evaluation" && !allowNull {
		return ErrInvalid
	}
	delim, ok := t.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			k, e := d.Token()
			name, ok := k.(string)
			if e != nil || !ok || seen[name] {
				return ErrInvalid
			}
			seen[name] = true
			if scan(d, name, depth+1, allowNull) != nil {
				return ErrInvalid
			}
		}
		end, e := d.Token()
		if e != nil || end != json.Delim('}') {
			return ErrInvalid
		}
	case '[':
		for d.More() {
			if scan(d, "", depth+1, allowNull) != nil {
				return ErrInvalid
			}
		}
		end, e := d.Token()
		if e != nil || end != json.Delim(']') {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	return nil
}
func exactFields(raw []byte, names ...string) bool {
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil || len(obj) != len(names) {
		return false
	}
	for _, name := range names {
		if _, ok := obj[name]; !ok {
			return false
		}
	}
	return true
}

func Load(path string) (*Config, error) {
	raw, err := privateinput.ReadFile(path, MaxConfigBytes)
	if err != nil {
		return nil, ErrInvalid
	}
	defer clear(raw)
	var c Config
	if !exactFields(raw, "format", "eval_run_id", "reservation_ref", "model_tag", "header_mode", "evaluation", "policy_file", "audit_key_file", "listen", "upstream", "evidence_dir", "control_dir") || StrictJSON(raw, &c) != nil || c.Format != Format {
		return nil, ErrInvalid
	}
	for _, s := range []string{c.EvalRunID, c.ReservationRef, c.ModelTag} {
		if !validRef(s) {
			return nil, ErrInvalid
		}
	}
	if !fileRef.MatchString(c.PolicyFile) || !fileRef.MatchString(c.AuditKeyFile) || c.PolicyFile == c.AuditKeyFile || c.PolicyFile == filepath.Base(path) || c.AuditKeyFile == filepath.Base(path) {
		return nil, ErrInvalid
	}
	switch c.HeaderMode {
	case "preserve", "protected_session_proposal":
		if c.Evaluation != nil {
			return nil, ErrInvalid
		}
	case "evaluation_caller_passthrough":
		var fields map[string]json.RawMessage
		_ = json.Unmarshal(raw, &fields)
		if c.Evaluation == nil || !exactFields(fields["evaluation"], "llm_backend", "target_kind") || (c.Evaluation.LLMBackend != "local" && c.Evaluation.LLMBackend != "api") || c.Evaluation.TargetKind != "public_practice" {
			return nil, ErrInvalid
		}
	default:
		return nil, ErrInvalid
	}
	host, port, err := net.SplitHostPort(c.Listen)
	n, e := strconv.Atoi(port)
	if err != nil || e != nil || host != "" || n < 1 || n > 65535 || strconv.Itoa(n) != port {
		return nil, ErrInvalid
	}
	if c.Upstream != "http://eval-meter-upstream:"+port {
		return nil, ErrInvalid
	}

	c.path = path
	c.PolicyPath = filepath.Join(filepath.Dir(path), c.PolicyFile)
	c.AuditKeyPath = filepath.Join(filepath.Dir(path), c.AuditKeyFile)
	c.ConfigSHA256 = digest(raw)
	for _, dir := range []string{filepath.Dir(path), c.ControlDir, c.EvidenceDir} {
		if privateinput.CheckDirectory(dir, true) != nil {
			return nil, ErrInvalid
		}
		info, e := os.Lstat(dir)
		if e != nil {
			return nil, ErrInvalid
		}
		c.dirs = append(c.dirs, pinned{path: dir, info: info})
	}
	for i, a := range c.dirs {
		for j := i + 1; j < len(c.dirs); j++ {
			b := c.dirs[j]
			if os.SameFile(a.info, b.info) || overlaps(a.path, b.path) {
				return nil, ErrInvalid
			}
		}
	}

	for i, p := range []pinned{{path: path, limit: MaxConfigBytes}, {path: c.PolicyPath, limit: MaxPolicyBytes}, {path: c.AuditKeyPath, limit: 32}} {
		data, e := privateinput.ReadFile(p.path, p.limit)
		if e != nil {
			return nil, ErrInvalid
		}
		p.digest = digest(data)
		if i == 0 && p.digest != c.ConfigSHA256 {
			clear(data)
			return nil, ErrInvalid
		}
		if i == 1 {
			var policy map[string]json.RawMessage
			if policyJSON(data, &policy) != nil {
				clear(data)
				return nil, ErrInvalid
			}
			c.PolicySHA256 = p.digest
		}
		if i == 2 && len(data) != 32 {
			clear(data)
			return nil, ErrInvalid
		}
		clear(data)
		p.info, e = os.Lstat(p.path)
		if e != nil {
			return nil, ErrInvalid
		}
		c.files = append(c.files, p)
	}
	c.original.wire, err = json.Marshal(&c)
	if err != nil {
		return nil, ErrInvalid
	}
	c.original.configSHA = c.ConfigSHA256
	c.original.policySHA = c.PolicySHA256
	c.original.policyPath = c.PolicyPath
	c.original.keyPath = c.AuditKeyPath
	if c.Check() != nil {
		return nil, ErrInvalid
	}
	return &c, nil
}

// Check은 사용 직전에 같은 파일·디렉터리와 정확한 바이트를 다시 확인한다.
func (c *Config) Check() error {
	if c == nil || len(c.files) != 3 || len(c.dirs) != 3 {
		return ErrInvalid
	}
	wire, err := json.Marshal(c)
	if err != nil || !bytes.Equal(wire, c.original.wire) || c.ConfigSHA256 != c.original.configSHA || c.PolicySHA256 != c.original.policySHA || c.PolicyPath != c.original.policyPath || c.AuditKeyPath != c.original.keyPath {
		return ErrInvalid
	}
	for _, p := range c.dirs {
		info, err := os.Lstat(p.path)
		if err != nil || !os.SameFile(info, p.info) || privateinput.CheckDirectory(p.path, true) != nil {
			return ErrInvalid
		}
	}
	for _, p := range c.files {
		data, err := readPinned(p)
		clear(data)
		if err != nil {
			return ErrInvalid
		}
	}
	return nil
}
func readPinned(p pinned) ([]byte, error) {
	data, err := privateinput.ReadFile(p.path, p.limit)
	if err != nil {
		return nil, ErrInvalid
	}
	info, err := os.Lstat(p.path)
	if err != nil || !os.SameFile(info, p.info) || digest(data) != p.digest {
		clear(data)
		return nil, ErrInvalid
	}
	return data, nil
}
func (c *Config) ReadPolicy() ([]byte, error) {
	if c.Check() != nil {
		return nil, ErrInvalid
	}
	return readPinned(c.files[1])
}
func (c *Config) ReadAuditKey() ([]byte, error) {
	if c.Check() != nil {
		return nil, ErrInvalid
	}
	return readPinned(c.files[2])
}

// 정책의 선택 필드는 null일 수 있다. 실제 스키마 검증은 정책 소비자가 맡는다.
func policyJSON(data []byte, target *map[string]json.RawMessage) error {
	if !utf8.Valid(data) {
		return ErrInvalid
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	if scan(d, "", 0, true) != nil {
		return ErrInvalid
	}
	if _, err := d.Token(); err != io.EOF {
		return ErrInvalid
	}
	if json.Unmarshal(data, target) != nil || *target == nil {
		return ErrInvalid
	}
	var mode string
	if json.Unmarshal((*target)["approval_mode"], &mode) != nil || mode != "preauthorized_scope" {
		return ErrInvalid
	}
	return nil
}

func overlaps(a, b string) bool {
	return a == b || strings.HasPrefix(a, b+string(filepath.Separator)) || strings.HasPrefix(b, a+string(filepath.Separator))
}
