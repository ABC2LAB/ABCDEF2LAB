package server

import (
	"context"
	"errors"
	"fmt"

	"github.com/Autumn-27/artex/llmpolicy"
)

// ErrControlledReadHostConfiguration is a startup failure, never permission to
// resume a task through the ordinary Worker.
var ErrControlledReadHostConfiguration = errors.New("server: controlled read host unavailable")

// ControlledReadHostOptions is trusted Go host assembly, not a model or HTTP
// request. Findings must supply real approval/KG provenance; the host installs the durable sink.
type ControlledReadHostOptions struct {
	Mode                 ControlledTaskModeHost
	Findings             ControlledReadFindingResolver
	LocalLLMManifestPath string
	// AuditSource optionally binds fresh audit reads during strict bootstrap.
	// Existing callers without it retain their supplied Source behavior.
	AuditSource *ControlledReadAuditSourceOptions `json:"-"`
}

// NewWithControlledReadHost installs strict mode admission and verified-read
// assembly before restored tasks and background admission loops can run.
// An optional local manifest uses the existing fail-closed local LLM loader;
// without it, the established DB/env provider selection remains in effect.
func NewWithControlledReadHost(ctx context.Context, m *Manager, skillDir, dataDir, keyDir string, options ControlledReadHostOptions) (*Server, error) {
	return newControlledReadHost(ctx, m, skillDir, dataDir, keyDir, options, nil, nil)
}

// ControlledReadDurableHostOptions is trusted Go assembly only. Missing or failed
// publication never selects the compatibility constructor's local-only path.
type ControlledReadDurableHostOptions struct {
	Host      ControlledReadHostOptions      `json:"-"`
	Publisher ControlledReadPublisherFactory `json:"-"`
}

func NewWithDurableControlledReadHost(ctx context.Context, m *Manager, skillDir, dataDir, keyDir string, options ControlledReadDurableHostOptions) (*Server, error) {
	if options.Publisher == nil {
		return nil, ErrControlledReadHostConfiguration
	}
	return newControlledReadHost(ctx, m, skillDir, dataDir, keyDir, options.Host, options.Publisher, nil)
}

func newControlledReadHost(ctx context.Context, m *Manager, skillDir, dataDir, keyDir string, options ControlledReadHostOptions, publisher ControlledReadPublisherFactory, revisions *controlledRevisionServices) (*Server, error) {
	if ctx == nil || m == nil || m.pg == nil || options.Findings == nil ||
		options.Mode.RootPath == "" || options.Mode.RootAnchor == nil || options.Mode.CheckRoot == nil || options.Mode.CheckTaskQuiescent == nil {
		return nil, ErrControlledReadHostConfiguration
	}
	var resolver *llmpolicy.ProfileResolver
	if options.LocalLLMManifestPath != "" {
		var err error
		resolver, err = llmpolicy.LoadProfileManifestFile(options.LocalLLMManifestPath)
		if err != nil {
			return nil, ErrLocalLLMPolicy
		}
	}
	return newServerConfigured(ctx, m, skillDir, dataDir, keyDir, resolver, func(s *Server) error {
		if err := s.ConfigureControlledTaskMode(options.Mode); err != nil {
			return fmt.Errorf("%w: %w", ErrControlledReadHostConfiguration, err)
		}
		if publisher != nil {
			if err := s.ConfigureControlledReadPublisher(publisher); err != nil {
				return fmt.Errorf("%w: %w", ErrControlledReadHostConfiguration, err)
			}
		}
		findings, err := s.bindControlledReadAuditSource(options.Findings, options.AuditSource)
		if err != nil {
			return fmt.Errorf("%w: %w", ErrControlledReadHostConfiguration, err)
		}
		if err := s.configureControlledReadFindings(findings, revisions); err != nil {
			return fmt.Errorf("%w: %w", ErrControlledReadHostConfiguration, err)
		}
		return nil
	})
}
