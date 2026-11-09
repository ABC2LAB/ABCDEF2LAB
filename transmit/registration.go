package transmit

import (
	"net/http"
	"net/url"
)

// CheckReadRegistration은 실제 송신과 같은 자원·세션·응답 규칙 검사를 재사용한다.
// 실행 scope·승인 수명·예산·감사 권위를 만들거나 대상에 접속하지 않는다.
func CheckReadRegistration(origin string, sessions []Session, resources []Resource) error {
	_, _, _, err := prepareReadRegistration(origin, sessions, resources)
	return err
}

func prepareReadRegistration(originText string, sessionInput []Session, resourceInput []Resource) (string, map[string]Session, map[string]binding, error) {
	origin, err := url.Parse(originText)
	if err != nil {
		return "", nil, nil, invalid("origin")
	}
	addr, err := authority(origin)
	if err != nil || origin.RawQuery != "" || origin.ForceQuery || origin.Path != "" && origin.Path != "/" {
		return "", nil, nil, invalid("origin")
	}
	sessions, err := copySessions(sessionInput)
	if err != nil {
		return "", nil, nil, err
	}
	if len(sessions) == 0 || len(resourceInput) == 0 {
		return "", nil, nil, invalid("empty registry")
	}
	resources := make(map[string]binding, len(resourceInput))
	for _, resource := range resourceInput {
		if !refPattern.MatchString(resource.Ref) || !refPattern.MatchString(resource.ApprovalRef) || !resource.ReadOnly || len(resource.Accounts) == 0 {
			return "", nil, nil, invalid("resource registration")
		}
		if _, exists := resources[resource.Ref]; exists {
			return "", nil, nil, invalid("duplicate resource")
		}
		u, err := url.Parse(resource.URL)
		if err != nil {
			return "", nil, nil, invalid("resource URL")
		}
		resourceAddr, err := authority(u)
		if err != nil || u.Scheme != origin.Scheme || resourceAddr != addr {
			return "", nil, nil, invalid("resource origin")
		}
		// Host와 dial 검사에 같은 정규화 authority를 사용한다.
		u.Host = addr
		if _, err := http.NewRequest(http.MethodGet, u.String(), nil); err != nil {
			return "", nil, nil, invalid("resource URL")
		}
		accounts := make(map[string]bool, len(resource.Accounts))
		for _, account := range resource.Accounts {
			if _, exists := sessions[account]; !exists {
				return "", nil, nil, invalid("approved account")
			}
			accounts[account] = true
		}
		rule, err := copyRule(resource.View)
		if err != nil {
			return "", nil, nil, err
		}
		resource.View = rule
		resource.Accounts = append([]string(nil), resource.Accounts...)
		resources[resource.Ref] = binding{resource, u, accounts}
	}
	return addr, sessions, resources, nil
}
