package main

import (
	"context"
	"errors"
	"net"
	"net/netip"

	"github.com/Autumn-27/artex/server"
)

// 접속 증거는 실행 전용이며 대상 승인 기록과 초안의 scope/digest를 바꾸지 않는다.
type productTargetConnection struct {
	IP        string
	SPKI      string
	Addresses []string
	jobID     string
}

func (l *urlLoadedProfile) prepareTargetConnection(ctx context.Context, resolver *net.Resolver, jobID string) error {
	if l.productTarget == nil {
		// Legacy profiles only perform their existing DNS pin check here.
		if l.dnsCheck(ctx, resolver) != nil {
			return errors.New("target_dns_mismatch")
		}
		return nil
	}
	if l.productRegistry == nil || l.productConnection != nil || len(l.policy.Rules) == 0 || !managementReference.MatchString(jobID) {
		return errPreparation
	}
	connection, err := l.productRegistry.connectionForStart(ctx, *l.productTarget, jobID)
	if err != nil {
		return err
	}
	ip, err := netip.ParseAddr(connection.IP)
	if err != nil {
		return errPreparation
	}
	for _, rule := range l.policy.Rules {
		if rule.Origin != l.productTarget.Target.Origin {
			return errPreparation
		}
	}
	rules := append([]server.E2HTTPFileRule(nil), l.policy.Rules...)
	for i := range rules {
		rules[i].ConnectIP = connection.IP
		rules[i].TLSSPKISHA256 = connection.SPKI
	}
	l.policy.Rules = rules
	l.address = ip
	l.productConnection = connection
	return nil
}

// 원문 오류는 내보내지 않고 검증한 고정 사유만 준비 Job에 싣는다.
func targetConnectionFailure(err error) string {
	if err != nil {
		switch err.Error() {
		case "target_approval_changed", "ownership_not_verified", "target_dns_unavailable", "target_dns_mismatch", "target_address_denied", "target_unreachable", "target_tls_unverified", "target_connection_timeout":
			return err.Error()
		}
	}
	return "target_connection_unavailable"
}
