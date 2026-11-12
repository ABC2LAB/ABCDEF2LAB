package e2proxy

import (
	"net/http"
	"strings"
)

// EvaluationCallerContext is a privileged host declaration for a disposable,
// public-practice evaluation run. It does not attest deployment or grant scope.
// Freeze this tuple and HeaderMode in the policy fingerprint and run record.
type EvaluationCallerContext struct {
	RunRef     string `json:"run_ref"`
	LLMBackend string `json:"llm_backend"`
	TargetKind string `json:"target_kind"`
}

const (
	EvaluationPublicPractice = "public_practice"
	// 신뢰 host와 회차 기록이 같은 백엔드 식별값을 사용하도록 고정한다.
	EvaluationLLMBackendLocal = "local"
	EvaluationLLMBackendAPI   = "api"
)

func cloneEvaluationCallerContext(value *EvaluationCallerContext) *EvaluationCallerContext {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func validateEvaluationCallerContext(cfg Config) (*EvaluationCallerContext, error) {
	if cfg.HeaderMode != HeaderEvaluationCallerPassthrough {
		if cfg.Evaluation != nil {
			return nil, ErrConfig
		}
		return nil, nil
	}
	value := cfg.Evaluation
	if value == nil || value.RunRef != cfg.Scope.RunRef || value.TargetKind != EvaluationPublicPractice ||
		(value.LLMBackend != EvaluationLLMBackendLocal && value.LLMBackend != EvaluationLLMBackendAPI) {
		return nil, ErrConfig
	}
	return cloneEvaluationCallerContext(value), nil
}

// The default and protected-session paths still refuse caller credentials.
// In the evaluation path, never strip a credential and silently send anonymously.
func (e *Execution) callerCredentialsAllowed(headers http.Header) bool {
	present := headerPresent(headers, "Authorization") || headerPresent(headers, "Cookie")
	if !present {
		return true
	}
	if e.cfg.HeaderMode != HeaderEvaluationCallerPassthrough && e.cfg.HeaderMode != HeaderTestAccountPassthrough {
		return false
	}
	for name, values := range headers {
		if strings.EqualFold(name, "Authorization") || strings.EqualFold(name, "Cookie") {
			if len(values) == 0 {
				return false
			}
			for _, value := range values {
				if strings.TrimSpace(value) == "" {
					return false
				}
			}
		}
		if strings.EqualFold(name, "Connection") {
			for _, value := range values {
				for _, token := range strings.Split(value, ",") {
					token = strings.TrimSpace(token)
					if strings.EqualFold(token, "Authorization") || strings.EqualFold(token, "Cookie") {
						return false
					}
				}
			}
		}
	}
	return true
}
