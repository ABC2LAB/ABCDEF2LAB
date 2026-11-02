package llmpolicy

import (
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	maxDNSHostBytes  = 253
	maxDNSLabelBytes = 63
	maxTCPPort       = 65535
	defaultHTTPPort  = 80
	defaultHTTPSPort = 443
)

type origin struct {
	key     string
	literal netip.Addr
}

func parseOrigin(raw string, configuration bool) (origin, bool) {
	if raw == "" || len(raw) > maxURLBytes || !utf8.ValidString(raw) ||
		strings.ContainsAny(raw, "\\#") || strings.IndexFunc(raw, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) }) >= 0 {
		return origin{}, false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.Opaque != "" {
		return origin{}, false
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return origin{}, false
	}
	if configuration && ((u.Path != "" && u.Path != "/") || u.RawPath != "" || u.RawQuery != "" || u.ForceQuery) {
		return origin{}, false
	}
	query, err := url.QueryUnescape(u.RawQuery)
	if err != nil || unsafeDecoded(u.Path) || unsafeDecoded(query) {
		return origin{}, false
	}
	host := u.Hostname()
	// Unicode case conversion can turn characters such as the Kelvin sign into
	// ASCII. Reject them before folding so the policy never performs IDNA-like
	// normalization into an independently registered hostname.
	for i := range host {
		if host[i] >= utf8.RuneSelf {
			return origin{}, false
		}
	}
	host = strings.ToLower(host)
	a, addressErr := netip.ParseAddr(host)
	var literal netip.Addr
	if addressErr == nil {
		if a.Zone() != "" || a.Is6() != strings.HasPrefix(u.Host, "[") {
			return origin{}, false
		}
		host, literal = a.String(), a.Unmap()
	} else if strings.HasPrefix(u.Host, "[") || !validDNSName(host) {
		return origin{}, false
	}
	port := defaultHTTPPort
	if scheme == "https" {
		port = defaultHTTPSPort
	}
	if rawPort := u.Port(); rawPort != "" {
		if !decimal(rawPort) || (len(rawPort) > 1 && rawPort[0] == '0') {
			return origin{}, false
		}
		parsed, err := strconv.ParseUint(rawPort, 10, 16)
		if err != nil || parsed == 0 || parsed > maxTCPPort {
			return origin{}, false
		}
		port = int(parsed)
	} else if strings.HasSuffix(u.Host, ":") {
		return origin{}, false
	}
	return origin{key: scheme + "://" + net.JoinHostPort(host, strconv.Itoa(port)), literal: literal}, true
}

func unsafeDecoded(value string) bool {
	return !utf8.ValidString(value) || strings.ContainsRune(value, '\\') || strings.IndexFunc(value, unicode.IsControl) >= 0
}

func validDNSName(host string) bool {
	if host == "" || len(host) > maxDNSHostBytes {
		return false
	}
	alternativeIP := true
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > maxDNSLabelBytes || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for i := range label {
			c := label[i]
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
		if !decimal(label) && !hexadecimal(label) {
			alternativeIP = false
		}
	}
	return !alternativeIP
}

func decimal(value string) bool {
	if value == "" {
		return false
	}
	for i := range value {
		if value[i] < '0' || value[i] > '9' {
			return false
		}
	}
	return true
}

func hexadecimal(value string) bool {
	if !strings.HasPrefix(value, "0x") || len(value) <= len("0x") {
		return false
	}
	for i := len("0x"); i < len(value); i++ {
		if c := value[i]; !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
