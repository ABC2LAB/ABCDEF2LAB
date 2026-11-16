package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"regexp"
	"time"
)

// generation-verify 역할(탐색 분리 배치 결정안 D(b)·§7): 제품(O)이 보호 저장소에 남긴 OpenRouter 호출 기록
// (llmpolicy.OpenRouterCallRecord, 게이트웨이 llm-requests 행과 같은 칸·어휘)을 표준 입력으로 받아, 게이트웨이와 같은
// generation 조회(generationVerifier)로 확인하고 게이트웨이와 같은 llm-verified.json 표식을 쓴다. 러너는 원본(B)
// 게이트웨이 표식과 같은 판정(judge_llm)을 한다. 이 역할은 엔진 트래픽을 받지 않고 리슨하지도 않는다.
const (
	roleGenerationVerify = "generation-verify"
	// maxCallRecordBytes는 표준 입력으로 받는 호출 기록 전체 상한이다. 제품 파일 상한(승인 요청 수 × 512바이트)보다 넉넉하다.
	maxCallRecordBytes = 64 << 20
	// maxCallRecordLine은 한 줄 상한이다(제품 줄 상한 512바이트와 같다).
	maxCallRecordLine = 512
)

var callRecordFields = []string{"seq", "decision", "method", "status", "started_at", "completed_at",
	"request_bytes", "response_bytes", "generation_id", "unfinished_end"}

var callRecordGenerationID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

// callRecordTally는 게이트웨이 finish()와 같은 규칙으로 센 값이다.
type callRecordTally struct {
	forwarded, upstreamErrors, missingIDs uint64
	ends                                  map[string]uint64
	ids                                   []string
}

// readCallRecords는 호출 기록 줄을 엄격하게 읽는다. 칸 집합·중복 키·순번(1부터 연속)·결정·끝 모양·상태·시각·id 모양이
// 하나라도 어긋나면 표식을 쓰지 않고 거부한다(검증할 수 없는 기록을 조용히 통과시키지 않는다).
func readCallRecords(r io.Reader) (callRecordTally, bool) {
	t := callRecordTally{ends: map[string]uint64{endClientClosedAfterDone: 0, endClientClosedBeforeDone: 0, endUpstreamCutAfterDone: 0, endUpstreamCutBeforeDone: 0}}
	data, err := io.ReadAll(io.LimitReader(r, maxCallRecordBytes+1))
	if err != nil || len(data) > maxCallRecordBytes || (len(data) > 0 && data[len(data)-1] != '\n') {
		return t, false
	}
	seen := map[string]bool{}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, maxCallRecordLine+1), maxCallRecordLine+1)
	var seq uint64
	for scanner.Scan() {
		seq++
		row, ok := parseCallRecord(scanner.Bytes())
		if !ok || row.Seq != seq {
			return t, false
		}
		switch row.Decision {
		case "forwarded":
			t.forwarded++
		default:
			t.upstreamErrors++
			t.ends[row.UnfinishedEnd]++
		}
		if row.GenerationID != "" {
			if !seen[row.GenerationID] {
				seen[row.GenerationID] = true
				t.ids = append(t.ids, row.GenerationID)
			}
		} else if row.Decision == "forwarded" && row.Status >= 200 && row.Status < 300 {
			t.missingIDs++
		}
	}
	return t, scanner.Err() == nil
}

func parseCallRecord(line []byte) (llmRow, bool) {
	var row llmRow
	if !exactKeys(line, callRecordFields, "unfinished_end") {
		return row, false
	}
	d := json.NewDecoder(bytes.NewReader(line))
	d.DisallowUnknownFields()
	if d.Decode(&row) != nil || d.More() {
		return row, false
	}
	started, e1 := time.Parse(time.RFC3339Nano, row.StartedAt)
	completed, e2 := time.Parse(time.RFC3339Nano, row.CompletedAt)
	ok := e1 == nil && e2 == nil && !completed.Before(started) && row.Method == http.MethodPost &&
		row.RequestBytes >= 1 && row.ResponseBytes >= 0 &&
		(row.GenerationID == "" || callRecordGenerationID.MatchString(row.GenerationID))
	switch row.Decision {
	case "forwarded":
		ok = ok && row.UnfinishedEnd == "" && row.Status >= 100 && row.Status <= 599
	case "upstream_error":
		_, known := map[string]bool{endClientClosedAfterDone: true, endClientClosedBeforeDone: true,
			endUpstreamCutAfterDone: true, endUpstreamCutBeforeDone: true}[row.UnfinishedEnd]
		ok = ok && known && (row.Status == 0 || row.Status >= 100 && row.Status <= 599)
	default:
		ok = false
	}
	return row, ok
}

// exactKeys는 JSON 객체 하나의 최상위 키가 중복 없이 fields 안에 있고, optional 말고는 모두 있는지 본다.
func exactKeys(line []byte, fields []string, optional string) bool {
	d := json.NewDecoder(bytes.NewReader(line))
	if tok, err := d.Token(); err != nil || tok != json.Delim('{') {
		return false
	}
	allowed := map[string]bool{}
	for _, f := range fields {
		allowed[f] = true
	}
	seen := map[string]bool{}
	for d.More() {
		tok, err := d.Token()
		key, isKey := tok.(string)
		if err != nil || !isKey || seen[key] || !allowed[key] {
			return false
		}
		seen[key] = true
		var skip json.RawMessage
		if d.Decode(&skip) != nil {
			return false
		}
	}
	if tok, err := d.Token(); err != nil || tok != json.Delim('}') {
		return false
	}
	for _, f := range fields {
		if !seen[f] && f != optional {
			return false
		}
	}
	return true
}

// runGenerationVerify는 호출 기록을 읽고, 게이트웨이와 같은 조회로 generation을 확인해 표식을 쓴다. 제품 기록에는
// 게이트웨이 같은 봉인이 없으므로 sealed_at·drained_at은 검증을 시작한 시각이다. 거부·봉인 뒤 거부·봉인 취소는
// 제품 경로에 없어 0이다. 제품이 기록을 남기지 못한 수(CallRecordFailures)는 이 표식 밖에서 러너가 따로 본다.
func runGenerationVerify(dir *os.File, verifier generationVerifier, provider string, sampling any, rows io.Reader, now func() time.Time) string {
	started := now()
	tally, ok := readCallRecords(rows)
	if !ok {
		return invalidInput
	}
	results := make([]generationResult, 0, len(tally.ids))
	for _, id := range tally.ids {
		results = append(results, verifier.lookup(id))
	}
	if err := writeMarker(dir, llmVerifiedName, verifiedMarker{
		Role: roleGenerationVerify, Backend: backendAPI, Provider: provider, Sampling: sampling,
		SealedAt: stamp(started), DrainedAt: stamp(started), VerifiedAt: stamp(now()),
		Forwarded: tally.forwarded, UpstreamErrors: tally.upstreamErrors, UpstreamEnds: tally.ends,
		MissingIDs: tally.missingIDs, Generations: results, LogComplete: true,
	}); err != nil {
		return storageUnavailable
	}
	return ""
}
