// Package targetaddr는 승인 대상 등록과 탐색 경계가 같이 쓰는 목적지 주소 분류다.
// 대상 등록(cmd/first-controlled-diagnosis)과 탐색 경계(exploreboundary)가 서로 다른 규칙으로 갈라지지 않게 한곳에 둔다.
package targetaddr

import "net/netip"

// metadataPrefixes는 클라우드 인스턴스 메타데이터·호스트 에이전트 주소다. link-local 전체는 Metadata가 따로 본다.
var metadataPrefixes = []netip.Prefix{
	netip.MustParsePrefix("fd00:ec2::254/128"),
	netip.MustParsePrefix("fd20:ce::254/128"),
	netip.MustParsePrefix("168.63.129.16/32"),
}

// specialPrefixes는 관리·문서·전환용 특수 대역이다. 사설 예외를 줘도 대상으로 쓰지 않는다.
var specialPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"), netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"), netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("224.0.0.0/3"), netip.MustParsePrefix("::/96"),
	netip.MustParsePrefix("64:ff9b::/96"), netip.MustParsePrefix("64:ff9b:1::/48"), netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("2001::/23"), netip.MustParsePrefix("2001:db8::/32"), netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("fec0::/10"), netip.MustParsePrefix("::ffff:0:0:0/96"), netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("5f00::/16"), netip.MustParsePrefix("3fff::/20"),
}

// Metadata는 link-local unicast(169.254.0.0/16·fe80::/10)와 알려진 메타데이터 주소다. multicast는 내부망으로 본다.
func Metadata(ip netip.Addr) bool {
	if !ip.IsValid() {
		return false
	}
	ip = ip.Unmap()
	if ip.IsLinkLocalUnicast() {
		return true
	}
	for _, prefix := range metadataPrefixes {
		if prefix.Contains(ip) {
			return true
		}
	}
	return false
}

// Allowed는 대상 주소로 쓸 수 있는지다. private이면 loopback·사설 대역도 받는다(등록 정책이 따로 좁힌다).
// 메타데이터·link-local·multicast·특수 대역·zone·IPv4-mapped 표기는 어떤 경우에도 받지 않는다.
func Allowed(ip netip.Addr, private bool) bool {
	if !ip.IsValid() || ip.Is4In6() || ip.Zone() != "" || ip.IsUnspecified() || ip.IsMulticast() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return false
	}
	for _, prefix := range metadataPrefixes {
		if prefix.Contains(ip) {
			return false
		}
	}
	if ip.IsLoopback() || ip.IsPrivate() {
		return private
	}
	for _, prefix := range specialPrefixes {
		if prefix.Contains(ip) {
			return false
		}
	}
	return ip.IsGlobalUnicast()
}
