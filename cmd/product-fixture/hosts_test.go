package main

import (
	"fmt"
	"net/netip"
	"strings"
	"testing"
)

// 기본값은 Juice Shop 수용시험이 쓰던 이름·주소와 같아야 한다(기존 수용시험 구성을 바꾸지 않음).
func TestDefaultFixtureHostsKeepAcceptanceNames(t *testing.T) {
	hosts, err := parseFixtureHosts(defaultFixtureHosts)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"juice-a.test": "172.31.253.20", "juice-b.test": "172.31.253.22", "juice.test": "172.31.253.20"}
	if len(hosts) != len(want) {
		t.Fatalf("hosts %d", len(hosts))
	}
	for _, h := range hosts {
		if want[h.name] != h.addr.String() {
			t.Fatalf("%s -> %s", h.name, h.addr)
		}
	}
	if strings.Join(fixtureHostNames(hosts), ",") != "juice-a.test,juice-b.test,juice.test" {
		t.Fatal(fixtureHostNames(hosts))
	}
}

func TestParseFixtureHostsPracticeTarget(t *testing.T) {
	hosts, err := parseFixtureHosts("vampi.test=172.31.253.20")
	if err != nil || len(hosts) != 1 || hosts[0].name != "vampi.test" || hosts[0].addr != netip.MustParseAddr("172.31.253.20") {
		t.Fatalf("%v %v", hosts, err)
	}
}

func TestParseFixtureHostsRejects(t *testing.T) {
	many := make([]string, maxFixtureHosts+1)
	for i := range many {
		many[i] = fmt.Sprintf("t%d.test=172.31.253.20", i)
	}
	for _, raw := range []string{
		"",
		"vampi.test",
		"vampi.test=",
		"=172.31.253.20",
		"vampi=172.31.253.20",
		"Vampi.test=172.31.253.20",
		"vampi.test=::1",
		"vampi.test=172.31.253.256",
		"vampi.test=172.31.253.20,vampi.test=172.31.253.22",
		"vampi.test=172.31.253.20,",
		"-vampi.test=172.31.253.20",
		strings.Join(many, ","),
	} {
		if hosts, err := parseFixtureHosts(raw); err == nil {
			t.Errorf("%q accepted: %v", raw, hosts)
		}
	}
}

// 인증서 CN에 대상 앱 이름이 들어가지 않는다(회차 기록에 남지 않게).
func TestFixtureLeafCommonNameIsNeutral(t *testing.T) {
	if strings.Contains(strings.ToLower(fixtureLeafCommonName), "juice") {
		t.Fatal(fixtureLeafCommonName)
	}
}
