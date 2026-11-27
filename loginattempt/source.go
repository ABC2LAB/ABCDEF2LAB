package loginattempt

import (
	"mime"
	"net/http"
	"strconv"

	"github.com/Autumn-27/artex/learnedlogin"
)

// sourceResponse lives only in the tracker memory. Its token and cookies are
// never copied into a login-attempt row or a learned method.
type sourceResponse struct {
	ref, context, path string
	status             int
	header             http.Header
	requestHeader      http.Header
	screen             learnedlogin.Screen
}

func (t *Tracker) rememberSourceLocked(req Request, resp Response) {
	if req.Method != http.MethodGet || req.RawQuery != "" || req.TransportRef == "" || resp.Status < 200 || resp.Status > 299 ||
		len(resp.Body) == 0 || len(resp.Body) >= maxRecentBodyBytes {
		return
	}
	media, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil || media != "application/json" && media != "text/html" && media != "application/xhtml+xml" {
		return
	}
	// A partial JSON or HTML prefix may already contain a token. The tracker
	// cannot prove completion for a body without an exact length.
	length := resp.Header.Get("Content-Length")
	n, lengthErr := strconv.ParseInt(length, 10, 64)
	if lengthErr != nil || n != int64(len(resp.Body)) {
		return
	}
	screen, reason := learnedlogin.ParseScreen(learnedlogin.Response{Status: resp.Status, Header: resp.Header, Body: resp.Body})
	if reason != "" {
		return
	}
	t.recentSources = append(t.recentSources, sourceResponse{ref: req.TransportRef, context: req.Context, path: cleanPath(req.Path),
		status: resp.Status, header: resp.Header.Clone(), requestHeader: req.Header.Clone(), screen: screen})
	if len(t.recentSources) > maxRecentBodies {
		t.recentSources = t.recentSources[len(t.recentSources)-maxRecentBodies:]
	}
}

func (t *Tracker) fieldSourceLocked(req Request, destination, value string) (RowFieldSource, bool) {
	if value == "" || req.RawQuery != "" {
		return RowFieldSource{}, false
	}
	var result RowFieldSource
	count := 0
	for _, source := range t.recentSources {
		if source.context != req.Context || source.path == cleanPath(req.Path) || source.ref == req.TransportRef {
			continue
		}
		// V2 issues exactly one bare GET. If the original issuance required
		// prior authentication, that request cannot be faithfully replayed.
		if !replayableSourceHeaders(source.requestHeader) {
			continue
		}
		typ, field, ok := source.screen.FindValueSource(value)
		if !ok {
			continue
		}
		jar := learnedlogin.Jar{}
		jar.AbsorbForLogin(learnedlogin.Response{Status: source.status, Header: source.header}, source.path, cleanPath(req.Path), t.cfg.Origin, t.cfg.Now())
		cookies := requestCookies(req.Header)
		if source.context == "" && len(jar) == 0 {
			// Direct/E② requests have no browser-context identifier. A
			// response cookie is required to bind this GET to the login.
			continue
		}
		bound := true
		for name, cookie := range jar {
			if cookies[name] != cookie {
				bound = false
				break
			}
		}
		if !bound {
			continue
		}
		count++
		result = RowFieldSource{Destination: destination, SourceMethod: http.MethodGet, SourcePath: source.path,
			SourceType: typ, SourceField: field, SourceRef: source.ref, SourceStatus: source.status}
	}
	return result, count == 1
}

// The E③ preparation GET sends no captured authentication context. Accept
// only ordinary representation-negotiation headers from the original GET;
// unknown or account-specific headers could be required by the issuer.
func replayableSourceHeaders(header http.Header) bool {
	for name := range header {
		canonical := http.CanonicalHeaderKey(name)
		switch canonical {
		case "Accept", "Accept-Language", "Accept-Encoding", "User-Agent", "Cache-Control", "Pragma", "Connection", "Dnt", "Upgrade-Insecure-Requests",
			"Sec-Fetch-Dest", "Sec-Fetch-Mode", "Sec-Fetch-Site", "Sec-Fetch-User":
		default:
			return false
		}
	}
	return true
}
