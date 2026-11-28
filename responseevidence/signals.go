package responseevidence

import (
	"regexp"
	"sort"
)

// 오류 신호 코드다. 응답 문구의 일부가 고정 패턴과 맞았다는 관찰일 뿐 취약점 판정이 아니다. 문구 원문은 남기지 않는다.
const (
	SignalStackTrace      = "stack_trace"
	SignalStackTraceField = "stack_trace_field"
	SignalSQLError        = "sql_error"
	SignalDebugPage       = "debug_page"
	SignalServerPath      = "server_path"
	SignalServerError     = "server_error_text"
	SignalExceptionName   = "exception_name"
)

type signalPattern struct {
	code string
	re   *regexp.Regexp
}

var signalPatterns = []signalPattern{
	{SignalStackTrace, regexp.MustCompile(`Traceback \(most recent call last\)|(?m)^\s+at [A-Za-z0-9_$.<>]+ ?\(|goroutine [0-9]+ \[|(?m)^panic: |(?m)^\s+File "[^"]+", line [0-9]+`)},
	{SignalSQLError, regexp.MustCompile(`(?i)SQLSTATE\[|syntax error at or near|You have an error in your SQL syntax|\bORA-[0-9]{5}\b|SQLITE_ERROR|sqlite3\.OperationalError|Unclosed quotation mark|pg_query\(\)|mysql_fetch|SequelizeDatabaseError`)},
	{SignalDebugPage, regexp.MustCompile(`Whitelabel Error Page|Django Version:|Werkzeug Debugger|Laravel\s+Ignition|DEBUG = True|Express\s+[0-9.]+\s+Error`)},
	{SignalServerPath, regexp.MustCompile(`(?:^|[\s"'(])(?:/home/[A-Za-z0-9_.-]+/|/var/www/|/usr/src/app/|/opt/[A-Za-z0-9_.-]+/|[A-Za-z]:\\(?:inetpub|Users|Program Files)\\)|/node_modules/`)},
	{SignalServerError, regexp.MustCompile(`(?i)Internal Server Error|Unhandled exception|An unexpected error occurred`)},
	{SignalExceptionName, regexp.MustCompile(`\b(?:[A-Z][A-Za-z0-9]+\.)*[A-Z][A-Za-z0-9]*(?:Exception|Error)\b:`)},
}

// signalScanBytes는 신호를 찾을 최대 길이다. 정규식 비용을 묶는다(RE2라 역추적 폭발은 없다).
const signalScanBytes = 256 << 10

func detectSignals(text []byte, _ string) []string {
	if len(text) > signalScanBytes {
		text = text[:signalScanBytes]
	}
	var out []string
	for _, p := range signalPatterns {
		if p.re.Match(text) {
			out = append(out, p.code)
		}
	}
	sort.Strings(out)
	return out
}

func addSignal(signals []string, code string) []string {
	for _, s := range signals {
		if s == code {
			return signals
		}
	}
	signals = append(signals, code)
	sort.Strings(signals)
	return signals
}
