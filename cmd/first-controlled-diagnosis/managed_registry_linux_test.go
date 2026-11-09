//go:build linux

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/Autumn-27/artex/diagnosisapi"
)

func managementRegistryFixture(t *testing.T) (managedRegistry, managementRegistryDocument, diagnosisapi.Draft) {
	t.Helper()
	c := diagnosisapi.Configuration{TargetRef: "owned-loopback", ScopeRef: "read-only", ModelProfileRef: "synthetic-model", ApprovalMode: "confirm_every_request", Budget: diagnosisapi.Budget{Requests: 2, Tokens: 100, DurationSeconds: 60}}
	digest, err := diagnosisapi.ConfigurationDigest(c)
	if err != nil {
		t.Fatal(err)
	}
	d := diagnosisapi.Draft{ID: "draft-fixture", Revision: 1, Configuration: c, SourceDigest: digest, State: "draft"}
	doc := managementRegistryDocument{Format: managementRegistryFormat, Profiles: []diagnosisapi.Profile{{ID: "profile", Label: "합성", Configuration: c}}, Approvals: []managementApproval{}}
	dir := t.TempDir()
	if os.Chmod(dir, 0700) != nil {
		t.Fatal("directory")
	}
	r := managedRegistry{path: filepath.Join(dir, "operator-registry.json")}
	writeManagementRegistry(t, r, doc)
	return r, doc, d
}
func writeManagementRegistry(t *testing.T, r managedRegistry, doc managementRegistryDocument) {
	t.Helper()
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if os.WriteFile(r.path, raw, 0600) != nil {
		t.Fatal("write fixture")
	}
}
func TestManagedRegistryFreshExactBinding(t *testing.T) {
	r, doc, d := managementRegistryFixture(t)
	b, err := r.Approval(t.Context(), "ARTEX", d)
	if err != nil || b != nil {
		t.Fatal("unapproved")
	}
	doc.Approvals = []managementApproval{{Principal: "ARTEX", DraftID: d.ID, Revision: d.Revision, SourceDigest: d.SourceDigest, TaskID: "42", PreparationRef: "prepared-fixture", ExpiresAt: time.Now().Add(time.Minute)}}
	writeManagementRegistry(t, r, doc)
	b, err = r.Approval(t.Context(), "ARTEX", d)
	if err != nil || b == nil || b.TaskID != "42" {
		t.Fatal("fresh approval")
	}
	d.Revision++
	b, err = r.Approval(t.Context(), "ARTEX", d)
	if err != nil || b != nil {
		t.Fatal("revision crossed")
	}
	d.Revision--
	d.SourceDigest = "different"
	b, err = r.Approval(t.Context(), "ARTEX", d)
	if err != nil || b != nil {
		t.Fatal("digest crossed")
	}
	doc.Approvals = []managementApproval{}
	writeManagementRegistry(t, r, doc)
	d.SourceDigest, _ = diagnosisapi.ConfigurationDigest(d.Configuration)
	b, err = r.Approval(t.Context(), "ARTEX", d)
	if err != nil || b != nil {
		t.Fatal("withdrawal ignored")
	}
	if _, err = r.Profiles(t.Context(), "unrelated"); err == nil {
		t.Fatal("principal ignored")
	}
}
func TestManagedRegistryRejectsUnprotectedAndMalformed(t *testing.T) {
	r, doc, _ := managementRegistryFixture(t)
	if os.Chmod(r.path, 0644) != nil {
		t.Fatal("mode")
	}
	if _, err := r.read(); err == nil {
		t.Fatal("public file")
	}
	if os.Chmod(r.path, 0600) != nil {
		t.Fatal("mode")
	}
	raw, _ := json.Marshal(doc)
	for name, data := range map[string][]byte{
		"unknown":   append(bytes.TrimSuffix(raw, []byte("}")), []byte(`,"protected_path":"/tmp/no"}`)...),
		"duplicate": append(bytes.TrimSuffix(raw, []byte("}")), []byte(`,"format":"abc2lab.diagnosis-management-registry/v1"}`)...),
		"trailing":  append(raw, []byte("{}")...),
	} {
		t.Run(name, func(t *testing.T) {
			if os.WriteFile(r.path, data, 0600) != nil {
				t.Fatal("write")
			}
			if _, err := r.read(); err == nil {
				t.Fatal("malformed accepted")
			}
		})
	}
}
func TestInheritedGatewayRequiresPipes(t *testing.T) {
	for _, fds := range [][2]int{{0, 1}, {3, 3}, {-1, 9}} {
		if _, _, err := inheritedGateway(fds[0], fds[1]); err == nil {
			t.Fatal(fds)
		}
	}
	file, err := os.CreateTemp(t.TempDir(), "regular")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer read.Close()
	defer write.Close()
	fileFD, err := syscall.Dup(int(file.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	pipeFD, err := syscall.Dup(int(write.Fd()))
	if err != nil {
		syscall.Close(fileFD)
		t.Fatal(err)
	}
	if _, _, err := inheritedGateway(fileFD, pipeFD); err == nil {
		t.Fatal("regular file accepted")
	}
}
func TestManagementBadArgumentsDoNotOpenServices(t *testing.T) {
	var output, stderr bytes.Buffer
	for _, args := range [][]string{{}, {"-config", "/unread", "-registry", "/unread", "-gateway-read-fd", "0", "-gateway-write-fd", "1"}} {
		if runManagement(t.Context(), args, &output, &stderr) != 2 {
			t.Fatal("invalid arguments")
		}
	}
	if output.Len() != 0 {
		t.Fatal("service readiness claimed")
	}
}
