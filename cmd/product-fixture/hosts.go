package main

import (
	"errors"
	"net"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
)

// defaultFixtureHosts는 Juice Shop 수용시험이 쓰던 대상 이름과 앞단 주소다(compose.acceptance.yaml의 target-front·target-front-b).
const defaultFixtureHosts = "juice-a.test=172.31.253.20,juice-b.test=172.31.253.22,juice.test=172.31.253.20"

// fixtureLeafCommonName은 대상 앞단 인증서의 CN이다. 대상 앱 이름을 넣지 않는다(회차 기록에 앱 이름이 남지 않게).
const fixtureLeafCommonName = "Owned target fixtures"

// maxFixtureHosts는 한 fixture가 응답하는 대상 이름 수 상한이다. 수용시험 구성의 이름 수보다 넉넉하고 인증서를 키우지 않는 값이다.
const maxFixtureHosts = 16

type fixtureHost struct {
	name string
	addr netip.Addr
}

var fixtureHostName = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)+$`)

var errFixtureHosts = errors.New("fixture_hosts_invalid")

// parseFixtureHosts는 "이름=IPv4,…"를 읽는다. 소문자 DNS 이름(점 하나 이상)과 IPv4만 받고, 같은 이름은 거부한다.
func parseFixtureHosts(raw string) ([]fixtureHost, error) {
	parts := strings.Split(raw, ",")
	if raw == "" || len(parts) > maxFixtureHosts {
		return nil, errFixtureHosts
	}
	seen := map[string]bool{}
	hosts := make([]fixtureHost, 0, len(parts))
	for _, part := range parts {
		name, ip, ok := strings.Cut(part, "=")
		addr, e := netip.ParseAddr(ip)
		if !ok || !fixtureHostName.MatchString(name) || seen[name] || e != nil || !addr.Is4() {
			return nil, errFixtureHosts
		}
		seen[name] = true
		hosts = append(hosts, fixtureHost{name: name, addr: addr})
	}
	return hosts, nil
}

func fixtureHostNames(hosts []fixtureHost) []string {
	names := make([]string, len(hosts))
	for i, h := range hosts {
		names[i] = h.name
	}
	return names
}

var errFixtureUpstream = errors.New("fixture_upstream_invalid")

// upstreamDialAddress는 준비 확인이 연결할 upstream 주소다. 프록시(http.Transport)와 같게 포트가 없으면 scheme 기본값
// (http 80, https 443)을 쓴다. 그 밖의 scheme이나 호스트 없는 URL은 시작할 때 거부한다.
func upstreamDialAddress(u *url.URL) (string, error) {
	if u.Hostname() == "" || u.Scheme != "http" && u.Scheme != "https" {
		return "", errFixtureUpstream
	}
	port := u.Port()
	if port == "" {
		port = map[string]string{"http": "80", "https": "443"}[u.Scheme]
	}
	return net.JoinHostPort(u.Hostname(), port), nil
}
