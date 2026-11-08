// 승인된 기존 공급자의 수명과 실제 검증 경계만 조립하며 권한을 발급하지 않는다.
package server

import (
	"context"
	"github.com/Autumn-27/artex/controlledhost"
	"github.com/Autumn-27/artex/e3record"
	"github.com/Autumn-27/artex/evidencebind"
	"github.com/Autumn-27/artex/idor"
)

type ControlledReadAssemblyInput struct {
	Expected evidencebind.Context  `json:"-"`
	KG       idor.PolicyResolver   `json:"-"`
	Source   e3record.SourceReader `json:"-"`
}

type ControlledReadAssemblyInputResolver func(context.Context, ControlledReadVerificationRequest) (ControlledReadAssemblyInput, error)

type ControlledReadAssemblyHostConfig struct {
	Sources              controlledhost.Sources                  `json:"-"`
	Publisher            ControlledReadPublisherFactory          `json:"-"`
	AuditSource          ControlledReadAuditSourceOptions        `json:"-"`
	Inputs               ControlledReadAssemblyInputResolver     `json:"-"`
	FinalizationScope    ControlledReadFinalizationScopeProvider `json:"-"`
	FixtureRegistries    map[string]string                       `json:"-"`
	LocalLLMManifestPath string
}

type ControlledReadAssemblyHost struct {
	resources interface {
		revoke()
		close(context.Context) error
	}
	Lifetime *controlledhost.Lifetime          `json:"-"`
	Options  ControlledReadRevisionHostOptions `json:"-"`
}

func NewControlledReadAssemblyResolver(inputs ControlledReadAssemblyInputResolver, scope ControlledReadFinalizationScopeProvider) ControlledReadFindingResolver {
	if inputs == nil || nilFinalizationValue(scope) {
		return nil
	}
	return func(ctx context.Context, request ControlledReadVerificationRequest) (ControlledReadFindingPlan, error) {
		if ctx == nil || ctx.Err() != nil {
			return ControlledReadFindingPlan{}, ErrControlledFindingUnavailable
		}
		input, err := inputs(ctx, request)
		if err != nil {
			return ControlledReadFindingPlan{}, ErrControlledFindingUnavailable
		}
		if input.Source == nil || ctx.Err() != nil {
			return ControlledReadFindingPlan{}, ErrControlledFindingUnavailable
		}
		metadata := func(ctx context.Context, expected evidencebind.Context) (e3record.Source, error) {
			source, err := input.Source(ctx, expected)
			if err != nil || source.EvalRunID != "" {
				return e3record.Source{}, ErrControlledFindingUnavailable
			}
			return source, nil
		}
		plan, err := request.BindRealInputs(ctx, input.Expected, input.KG, metadata)
		if err != nil {
			return ControlledReadFindingPlan{}, ErrControlledFindingUnavailable
		}
		plan.FinalizationScope = scope
		return plan, nil
	}
}
