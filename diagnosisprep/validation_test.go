package diagnosisprep

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCanonicalInputStrictLocalValidation(t *testing.T) {
	good := map[string]string{"https://EXAMPLE.test:443": "https://example.test/", "https://example.test/read": "https://example.test/read", "https://[2001:db8::1]:8443/read": "https://[2001:db8::1]:8443/read"}
	for input, want := range good {
		t.Run(input, func(t *testing.T) {
			got, e := CanonicalInput(Input{TargetURL: input, ProfileID: "local"})
			if e != nil || got.TargetURL != want {
				t.Fatalf("%#v %v", got, e)
			}
		})
	}
	bad := []string{"http://example.test/", "https://[owned.example]/", "https://[127.0.0.1]/", "https://2001:db8::1:443/", "file:///etc/passwd", "https://user@example.test/", "https://example.test/#", "https://example.test/?", "https://example.test/?x=1", "https://example.test/%2fread", "https://example.test/%252fread", "https://example.test/a/../b", "https://example.test/./b", "https://example.test//read", "https://example.test/\\read", "https://example.test./", "https://예제.test/", "https://127.1/", "https://2130706433/", "https://0177.0.0.1/", "https://0x7f000001/", "https://[::ffff:127.0.0.1]/", "https://[fe80::1%25eth0]/", "https://example.test:0443/", "https://example.test:/", "https://example.test:0/", "https://example.test:65536/", " https://example.test/", "https://example.test/\n", "https://example.test/a b"}
	for _, input := range bad {
		t.Run(input, func(t *testing.T) {
			if _, e := CanonicalInput(Input{TargetURL: input, ProfileID: "local"}); e == nil {
				t.Fatal("ambiguous URL accepted")
			}
		})
	}
}
func TestStrictJSONRejectsNonCanonicalFields(t *testing.T) {
	valid := `{"version":"diagnosis-preparation.v1","request_id":"r","command":"create","principal":"owner","idempotency_key":"key","input":{"target_url":"https://example.test/","profile_id":"local"}}`
	var request Request
	if e := strictDecode([]byte(valid), &request); e != nil {
		t.Fatal(e)
	}
	bad := []string{
		strings.Replace(valid, `"version"`, `"Version"`, 1),
		strings.Replace(valid, `"request_id":"r"`, `"request_id":"r","request_id":"r"`, 1),
		strings.Replace(valid, `"profile_id":"local"`, `"profile_id":"local","profile_id":"other"`, 1),
		strings.Replace(valid, `"profile_id":"local"`, `"profile_id":"local","file_path":"/secret"`, 1),
		strings.Replace(valid, `"profile_id":"local"`, `"profile_id":null`, 1),
		valid + " {}", valid + "\n{}",
	}
	for i, raw := range bad {
		if e := strictDecode([]byte(raw), new(Request)); e == nil {
			t.Fatalf("accepted malformed JSON %d", i)
		}
	}
}
func TestResponseFactsAndUnknownAreIndependent(t *testing.T) {
	base := Job{ID: "prep-job", Revision: 1, SourceDigest: strings.Repeat("a", 64), Input: Input{TargetURL: "https://example.test/", ProfileID: "local"}, State: Unknown, Phase: Closure, TaskID: "42", PreparationStarted: true, AuthorityState: AuthorityNotIssued}
	for _, drain := range []bool{false, true} {
		for _, result := range []bool{false, true} {
			j := base
			j.DrainComplete = drain
			j.ResultReady = result
			if !validJob(j) {
				t.Fatalf("unknown facts collapsed: drain %v result %v", drain, result)
			}
		}
	}
	bad := base
	bad.Started = true
	if validJob(bad) {
		t.Fatal("started without consumed authority accepted")
	}
	bad = base
	bad.TaskID = ""
	bad.ResultReady = true
	if validJob(bad) {
		t.Fatal("result without task accepted")
	}
	bad = base
	bad.State = Completed
	bad.DrainComplete = true
	bad.ResultReady = true
	if validJob(bad) {
		t.Fatal("completed without actual start accepted")
	}
	base.State = Failed
	base.DrainComplete = true
	if !validJob(base) {
		t.Fatal("closed preparation failure rejected")
	}
}
func TestRequestAndResponseShape(t *testing.T) {
	r := Request{Version: Version, RequestID: "r", Principal: "owner", Command: Stop, JobID: "prep-job", ExpectedRevision: 2}
	if ValidateRequest(r) != nil {
		t.Fatal("valid revision rejected")
	}
	r.ExpectedRevision = 0
	if ValidateRequest(r) == nil {
		t.Fatal("missing stop revision accepted")
	}
	r.Command = Catalog
	r.JobID = ""
	r.Input = &Input{}
	if ValidateRequest(r) == nil {
		t.Fatal("catalog input accepted")
	}
	r.Input = nil
	response := Response{Version: Version, RequestID: "wrong"}
	if ValidateResponse(r, response) == nil {
		t.Fatal("wrong correlation accepted")
	}
	response.RequestID = r.RequestID
	response.Error = "private raw error /secret"
	if ValidateResponse(r, response) == nil {
		t.Fatal("arbitrary error accepted")
	}
	raw, _ := json.Marshal(response)
	if len(raw) == 0 {
		t.Fatal("marshal")
	}
}

func TestAuthorizeResponseRequiresMatchingOwnedJob(t *testing.T) {
	r := Request{Version: Version, RequestID: "r", Command: AuthorizeTask, Principal: "owner", TaskID: "42"}
	yes := true
	res := Response{Version: Version, RequestID: "r", Authorized: &yes}
	if ValidateResponse(r, res) == nil {
		t.Fatal("missing owned job accepted")
	}
	res.Job = &Job{ID: "prep-job", Revision: 1, SourceDigest: strings.Repeat("a", 64), Input: Input{TargetURL: "https://example.test/", ProfileID: "local"}, State: Unknown, Phase: Planning, TaskID: "42", PreparationStarted: true, AuthorityState: AuthorityNotIssued}
	if ValidateResponse(r, res) != nil {
		t.Fatal("owned job rejected")
	}
	res.Job.TaskID = "43"
	if ValidateResponse(r, res) == nil {
		t.Fatal("wrong task mapping accepted")
	}
	yes = false
	if ValidateResponse(r, res) == nil {
		t.Fatal("unauthorized task data leaked")
	}
}

func TestSourceDigestBindsCanonicalInputAndInstallation(t *testing.T) {
	input := Input{TargetURL: "https://example.test/read", ProfileID: "local"}
	installation := strings.Repeat("a", 64)
	first := SourceDigest(input, installation)
	if !digestPattern.MatchString(first) || SourceDigest(input, installation) != first {
		t.Fatal("unstable source digest")
	}
	if SourceDigest(input, strings.Repeat("b", 64)) == first {
		t.Fatal("installation is not bound")
	}
	input.ProfileID = "another"
	if SourceDigest(input, installation) == first {
		t.Fatal("profile is not bound")
	}
}
