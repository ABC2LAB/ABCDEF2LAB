package main

import (
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/Autumn-27/artex/diagnosisprep"
)

// 외부 전송 동의 — 설치 단위로 한 번(사용자 결정 2026-11-26). 외부 모델(OpenRouter)을 쓰는 회차는 대상 응답 · 도구 출력,
// 등록한 시험 계정의 아이디 · 비밀번호, 대상이 발급한 세션을 가리지 않고 제공자에게 보낸다(CLAUDE.md 규칙 2 · 8, DLP 폐기).
// 그래서 회차마다 묻지 않고 설치 조직이 이 문장에 한 번 동의한다. 새 설치는 설치 입력(external_transfer_consent)으로,
// 이미 설치된 곳은 관리자가 화면에서 한 번(external_consent_record) 남긴다. 둘 다 같은 상태 파일 하나다.
//   - 파일은 상태 디렉터리의 external-transfer-consent-<문장 해시 앞 16자>.json(0600)이고 한 번 만들면 바꾸지 않는다.
//     문장이 바뀌면 파일 이름이 달라 옛 동의는 지금 문장의 동의가 아니다(다시 한 번 받는다).
//   - 기록에는 문장의 SHA-256 · 출처(install · admin) · 주체 · 시각만 남긴다.
//   - 동의가 없는 설치의 외부 모델 회차는 검토 사유 external_transfer_consent_required로 시작하지 않는다.

// productExternalTransferStatement는 설치 조직이 동의하는 외부 전송 문장이다. 화면 · 설치 입력은 이 문장을 그대로 쓴다.
const productExternalTransferStatement = "이 설치에서 외부 모델(OpenRouter)을 쓰는 점검은 대상 응답 · 도구 출력, 등록한 시험 계정의 아이디 · 비밀번호, 대상이 발급한 세션(쿠키 · 토큰)을 가리지 않고 외부 모델 제공자에게 보냅니다. 계정은 시험 계정만 등록하며, 외부로 보낸 정보에 대한 책임은 설치 조직에 있습니다."

const (
	productExternalConsentFormat = "abc2lab.external-transfer-consent/v1"
	// productExternalConsentMaxBytes는 동의 기록 파일 읽기 상한이다(기록은 수백 바이트).
	productExternalConsentMaxBytes = 4 << 10
	// productExternalConsentNameDigest는 파일 이름에 넣는 문장 해시 길이다(문장 판 구분용, 기록 안에는 전체 해시).
	productExternalConsentNameDigest = 16
	productExternalConsentInstall    = "install"
	productExternalConsentAdmin      = "admin"
)

type productExternalConsentRecord struct {
	Format          string    `json:"format"`
	StatementSHA256 string    `json:"statement_sha256"`
	Source          string    `json:"source"`
	Actor           string    `json:"actor"`
	At              time.Time `json:"at"`
}

var errExternalConsentMissing = errors.New("external_transfer_consent_missing")

func productExternalTransferStatementSHA256() string {
	return statementSHA256(productExternalTransferStatement)
}

func productExternalConsentPath(stateDir string) string {
	return filepath.Join(stateDir, "external-transfer-consent-"+productExternalTransferStatementSHA256()[:productExternalConsentNameDigest]+".json")
}

// readProductExternalConsent는 지금 문장의 동의 기록을 읽는다. 없으면 errExternalConsentMissing, 읽을 수 없거나 형식이
// 틀리면 errPreparation이다(동의 없음으로 읽지 않는다 — 둘 다 외부 회차를 시작하지 않는 것은 같다).
func readProductExternalConsent(stateDir string) (productExternalConsentRecord, error) {
	var rec productExternalConsentRecord
	f, err := os.Open(productExternalConsentPath(stateDir))
	if errors.Is(err, fs.ErrNotExist) {
		return rec, errExternalConsentMissing
	}
	if err != nil {
		return rec, errPreparation
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return rec, errPreparation
	}
	raw, err := io.ReadAll(io.LimitReader(f, productExternalConsentMaxBytes+1))
	if err != nil || len(raw) > productExternalConsentMaxBytes || decodeLaunchJSON(raw, &rec) != nil {
		return productExternalConsentRecord{}, errPreparation
	}
	if rec.Format != productExternalConsentFormat || rec.StatementSHA256 != productExternalTransferStatementSHA256() ||
		(rec.Source != productExternalConsentInstall && rec.Source != productExternalConsentAdmin) || !managementReference.MatchString(rec.Actor) || rec.At.IsZero() {
		return productExternalConsentRecord{}, errPreparation
	}
	return rec, nil
}

// writeProductExternalConsent는 지금 문장의 동의를 한 번 남긴다. 이미 있으면 그 기록을 돌려준다(먼저 남긴 동의가 이긴다).
// 임시 파일에 다 쓰고 fsync한 뒤 link로 이름을 붙여, 반쯤 쓴 기록이 동의로 읽히지 않게 한다.
func writeProductExternalConsent(stateDir, source, actor string, at time.Time) (productExternalConsentRecord, error) {
	if rec, err := readProductExternalConsent(stateDir); err == nil || !errors.Is(err, errExternalConsentMissing) {
		return rec, err
	}
	rec := productExternalConsentRecord{Format: productExternalConsentFormat, StatementSHA256: productExternalTransferStatementSHA256(), Source: source, Actor: actor, At: at.UTC()}
	if (source != productExternalConsentInstall && source != productExternalConsentAdmin) || !managementReference.MatchString(actor) || at.IsZero() {
		return productExternalConsentRecord{}, errPreparation
	}
	tmp, err := os.CreateTemp(stateDir, ".external-transfer-consent-*")
	if err != nil {
		return productExternalConsentRecord{}, errPreparation
	}
	name := tmp.Name()
	defer func() {
		if err := os.Remove(name); err != nil && !errors.Is(err, fs.ErrNotExist) {
			log.Print("[preparation] 외부 전송 동의 임시 파일 삭제 실패")
		}
	}()
	raw, err := json.Marshal(rec)
	if err == nil {
		_, err = tmp.Write(raw)
	}
	if err = errors.Join(err, tmp.Chmod(0o600), tmp.Sync(), tmp.Close()); err != nil {
		return productExternalConsentRecord{}, errPreparation
	}
	if err := os.Link(name, productExternalConsentPath(stateDir)); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return readProductExternalConsent(stateDir)
		}
		return productExternalConsentRecord{}, errPreparation
	}
	dir, err := os.Open(stateDir)
	if err != nil {
		return productExternalConsentRecord{}, errPreparation
	}
	if err := errors.Join(dir.Sync(), dir.Close()); err != nil {
		return productExternalConsentRecord{}, errPreparation
	}
	return rec, nil
}

// externalTransferRequired는 이 설치에 외부 모델 프로필이 있는지다(없으면 동의가 필요 없다).
func (p *productRuntime) externalTransferRequired() bool {
	for _, m := range productModels(p.config) {
		_, cfg, _, _, err := p.model(m.ID)
		if err == nil && cfg.ModelExecutionMode == "openrouter" {
			return true
		}
	}
	return false
}

// externalConsent는 화면에 보이는 설치의 외부 전송 동의 상태다. 기록을 읽을 수 없으면 오류다(동의 없음으로 보이지 않는다).
func (p *productRuntime) externalConsent() (*diagnosisprep.ProductExternalConsent, error) {
	if !p.externalTransferRequired() {
		return &diagnosisprep.ProductExternalConsent{}, nil
	}
	out := &diagnosisprep.ProductExternalConsent{Required: true, Statement: productExternalTransferStatement}
	rec, err := readProductExternalConsent(p.config.StateDir)
	if errors.Is(err, errExternalConsentMissing) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	at := rec.At
	out.Consented, out.Source, out.ConsentedBy, out.ConsentedAt = true, rec.Source, rec.Actor, &at
	return out, nil
}

// recordExternalConsent는 관리자 화면의 동의를 남긴다. 외부 모델이 없는 설치는 남길 것이 없다(external_transfer_not_installed).
func (p *productRuntime) recordExternalConsent(principal string, confirmed bool, now time.Time) (*diagnosisprep.ProductExternalConsent, string, error) {
	if !confirmed {
		return nil, "statement_required", nil
	}
	if !p.externalTransferRequired() {
		return nil, "external_transfer_not_installed", nil
	}
	p.consentMu.Lock()
	defer p.consentMu.Unlock()
	if _, err := writeProductExternalConsent(p.config.StateDir, productExternalConsentAdmin, principal, now); err != nil {
		return nil, "", err
	}
	c, err := p.externalConsent()
	return c, "", err
}

// externalTransferConsented는 외부 모델 회차를 시작해도 되는지다(지금 문장의 동의 기록이 있음).
func (p *productRuntime) externalTransferConsented() bool {
	_, err := readProductExternalConsent(p.config.StateDir)
	return err == nil
}
