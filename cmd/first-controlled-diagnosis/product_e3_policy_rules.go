package main

import (
	"strconv"
	"strings"

	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/e3contract"
	"github.com/Autumn-27/artex/internal/pathscope"
	"github.com/Autumn-27/artex/server"
)

// productE3PolicyRules는 대상 승인·동결 선언과 시험 계정 역할이 맞을 때만 목적별 규칙을 발급한다.
// 호출자는 같은 선언을 회차에 동결하고, 결과 정책 전체를 authority.json의 digest에 묶는다.
func productE3PolicyRules(policy server.E2HTTPFilePolicy, target productTargetRecord, declaration e3contract.Declaration) (server.E2HTTPFilePolicy, int) {
	if !target.ReversibleWrites || target.WriteConsent == "" || policy.Auth == nil ||
		declaration.Validate() != nil || declarationScopeField(target, declaration) != "" ||
		declaration.OwnerAccountLabel == "" || declaration.CrossAccountLabel == "" ||
		declaration.OwnerAccountLabel == declaration.CrossAccountLabel || !declaration.AccountsSamePrivilege {
		return policy, 0
	}
	labels := map[string]bool{}
	for _, account := range policy.Auth.Accounts {
		labels[account.Label] = true
	}
	if !labels[declaration.OwnerAccountLabel] || !labels[declaration.CrossAccountLabel] {
		return policy, 0
	}
	// 현재 E② 계약은 각 목적 규칙의 메서드도 Worker 승인 범위에 있어야 한다.
	// 이 조건이 없으면 실행 정책 자체가 거부되므로, 확정 단계만 0몫으로 둔다.
	covered := func(path, method string, read bool) bool {
		matchPath := path
		if read {
			matchPath, _, _ = strings.Cut(path, "/{id}")
		}
		for _, rule := range policy.Rules {
			if rule.Purpose != "" || rule.Origin != target.Target.Origin || rule.ConnectIP != target.SelectedIP ||
				rule.TLSSPKISHA256 != target.Target.TLSSPKISHA256 || read && rule.PathMatch == "exact" {
				continue
			}
			methodAllowed := false
			for _, allowed := range rule.Methods {
				methodAllowed = methodAllowed || allowed == method
			}
			if methodAllowed && (pathscope.Scope{Path: rule.PathPrefix, Mode: pathscope.Mode(rule.PathMatch)}).Allows(matchPath, false) {
				return true
			}
		}
		return false
	}
	if !covered(declaration.Identity.Path, "GET", false) {
		return policy, 0
	}
	for _, kind := range declaration.Types {
		if kind.Confirmable() && (!covered(kind.Create.Path, kind.Create.Method, false) ||
			!covered(kind.Read.PathTemplate, "GET", true)) {
			return policy, 0
		}
	}
	add := func(ref, purpose, path, method string) {
		policy.Rules = append(policy.Rules, server.E2HTTPFileRule{Ref: ref, Purpose: purpose,
			Origin: target.Target.Origin, ConnectIP: target.SelectedIP, TLSSPKISHA256: target.Target.TLSSPKISHA256,
			PathPrefix: path, PathScopeRef: ref + "-path", Methods: []string{method},
			ApprovalDisplay: server.RequestApprovalDisplay{QueryKeys: []string{}, JSONFields: []string{}}})
	}
	count := 0
	for _, kind := range declaration.Types {
		if kind.Confirmable() {
			count++
		}
	}
	if count == 0 {
		return policy, 0
	}
	// 기존 Worker 규칙의 ref와 겹치면 실행 정책 전체가 무효가 되므로 확정 몫만 닫는다.
	refs := make(map[string]bool, len(policy.Rules)+1+count*2)
	for _, rule := range policy.Rules {
		refs[rule.Ref] = true
	}
	if refs["e3-identity"] {
		return policy, 0
	}
	for i, kind := range declaration.Types {
		if !kind.Confirmable() {
			continue
		}
		ref := "e3-" + strconv.Itoa(i)
		if refs[ref+"-create"] || refs[ref+"-read"] {
			return policy, 0
		}
	}
	add("e3-identity", e2proxy.PurposeE3Identity, declaration.Identity.Path, "GET")
	for i, kind := range declaration.Types {
		if !kind.Confirmable() {
			continue
		}
		ref := "e3-" + strconv.Itoa(i)
		add(ref+"-create", e2proxy.PurposeE3Create, kind.Create.Path, kind.Create.Method)
		add(ref+"-read", e2proxy.PurposeE3Read, kind.Read.PathTemplate, "GET")
	}
	policy.E3WriteConsent, policy.E3Resettable = true, true
	return policy, count
}
