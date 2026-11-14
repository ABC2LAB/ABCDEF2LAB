package main

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

type productContactJournal interface {
	Check() error
	Append(json.RawMessage) error
	Close() error
}

// HTTP 전송 감사와 별도로 TLS 사전 접촉 시도·결과를 보호 저널에 결속한다.
// 원문 인증서·오류·키는 저장하지 않는다. 감사 저장 실패 시 접촉·승인을 진행하지 않는다.
var errTargetCertificateExpired = errors.New("target_approval_expired")

type productTLSContact struct {
	JobID         string    `json:"job_id,omitempty"`
	TLSSPKISHA256 string    `json:"tls_spki_sha256,omitempty"`
	Format        string    `json:"format"`
	ID            string    `json:"id"`
	TargetID      string    `json:"target_id"`
	Revision      uint64    `json:"revision"`
	Stage         string    `json:"stage"`
	Phase         string    `json:"phase"`
	Host          string    `json:"host"`
	IP            string    `json:"ip"`
	Port          int       `json:"port"`
	At            time.Time `json:"at"`
	Result        string    `json:"result,omitempty"`
}

func (r *productTargetRegistry) probeWithAudit(ctx context.Context, stage string, rec productTargetRecord, host, ip string) (string, time.Time, error) {
	return r.probeWithAuditForRun(ctx, stage, rec, host, ip, "")
}
func (r *productTargetRegistry) probeWithAuditForRun(ctx context.Context, stage string, rec productTargetRecord, host, ip, jobID string) (string, time.Time, error) {
	if r.check() != nil || r.probe == nil || r.reach == nil {
		return "", time.Time{}, errPreparation
	}
	event := productTLSContact{JobID: jobID, Format: "abc2lab.product-tls-contact/v1", ID: "tls-" + productRandom(16), TargetID: rec.Target.ID, Revision: rec.Target.Revision, Stage: stage, Phase: "attempt", Host: host, IP: ip, Port: rec.Target.Port, At: r.now()}
	raw, err := json.Marshal(event)
	if err != nil || r.contacts.Append(raw) != nil {
		return "", time.Time{}, errPreparation
	}
	// DNS·감사 기록 사이에 철회된 시작 접촉은 실제 연결 전에 다시 막는다.
	if stage == "start" && r.current(rec) != nil {
		return "", time.Time{}, errPreparation
	}
	var digest string
	var until time.Time
	var probeErr error
	if productTargetPlain(rec.Target) {
		// http 대상은 연결만 확인한다. TLS 고정값·인증서 만료가 없다(C22).
		probeErr = r.reach(ctx, ip, rec.Target.Port)
	} else {
		digest, until, probeErr = r.probe(ctx, host, ip, rec.Target.Port)
	}
	if probeErr == nil && !productTargetPlain(rec.Target) {
		if !urlDigestValid(digest) {
			probeErr = errPreparation
		} else if !until.After(r.now()) {
			probeErr = errTargetCertificateExpired
		}
	}
	if probeErr == nil {
		event.TLSSPKISHA256 = digest
	}
	event.Phase = "completed"
	event.At = r.now()
	event.Result = "verified"
	if probeErr != nil {
		event.Result = "unverified"
	}
	raw, err = json.Marshal(event)
	if err != nil || r.contacts.Append(raw) != nil {
		return "", time.Time{}, errPreparation
	}
	return digest, until, probeErr
}
