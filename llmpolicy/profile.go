package llmpolicy

import (
	"errors"
	"net/netip"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	maxProfileBindings    = 64
	maxProfileFormatBytes = 64
	maxProfileModelBytes  = 1024
)

var (
	ErrInvalidProfilePolicy  = errors.New("llmpolicy: invalid profile policy")
	ErrInvalidProfileTarget  = errors.New("llmpolicy: invalid profile target")
	ErrProfilePolicyMissing  = errors.New("llmpolicy: profile policy missing")
	ErrProfileTargetMismatch = errors.New("llmpolicy: profile target mismatch")
)

// ProfileTarget identifies the effective endpoint configuration selected by the
// host. It is not a full provider snapshot, profile revision or authorization.
// Credentials and request/routing options deliberately do not enter this tuple.
type ProfileTarget struct {
	ID      int64
	Format  string
	Model   string
	BaseURL string
}

// ProfilePin comes only from host-validated operator policy, independently of
// editable DB profiles, model/task input and DNS answers. This type authenticates
// no file or caller; the deployment-policy loader is a separate boundary.
type ProfilePin struct {
	Target    ProfileTarget
	Addresses []netip.Addr
}

type profilePolicy struct {
	target ProfileTarget
	policy Policy
}

// ProfileResolver is an immutable local-only registry. A new configuration
// requires a new resolver; failed/missing resolution never selects a fallback.
type ProfileResolver struct{ profiles map[int64]profilePolicy }

// NewProfileResolver compiles all operator bindings or returns no registry.
// Policy.New owns origin/IP validation; BaseURL paths remain tuple metadata.
func NewProfileResolver(pins []ProfilePin) (*ProfileResolver, error) {
	if len(pins) == 0 || len(pins) > maxProfileBindings {
		return nil, ErrInvalidProfilePolicy
	}
	profiles := make(map[int64]profilePolicy, len(pins))
	for _, pin := range pins {
		o, valid := profileOrigin(pin.Target)
		if !valid {
			return nil, ErrInvalidProfilePolicy
		}
		if _, duplicate := profiles[pin.Target.ID]; duplicate {
			return nil, ErrInvalidProfilePolicy
		}
		// A provider BaseURL can have /v1; only its path-free canonical origin
		// belongs to the endpoint policy. New validates and copies the pins.
		policy, err := New([]Endpoint{{Origin: o.key, Addresses: pin.Addresses}})
		if err != nil {
			return nil, ErrInvalidProfilePolicy
		}
		profiles[pin.Target.ID] = profilePolicy{target: pin.Target, policy: *policy}
	}
	return &ProfileResolver{profiles: profiles}, nil
}

// Resolve checks the exact effective target and returns an independent Policy
// value. The host must carry that same configuration snapshot into the existing
// NewLocalProvider factory; this resolver never builds or switches providers.
func (r *ProfileResolver) Resolve(target ProfileTarget) (*Policy, error) {
	if _, valid := profileOrigin(target); !valid {
		return nil, ErrInvalidProfileTarget
	}
	if r == nil {
		return nil, ErrProfilePolicyMissing
	}
	bound, exists := r.profiles[target.ID]
	if !exists {
		return nil, ErrProfilePolicyMissing
	}
	if bound.target != target {
		return nil, ErrProfileTargetMismatch
	}
	// Keep the registry intact if the caller replaces its returned Policy value.
	// The shared private maps have no mutators and remain read-only.
	policy := bound.policy
	return &policy, nil
}

func profileOrigin(target ProfileTarget) (origin, bool) {
	if target.ID <= 0 || !validProfileValue(target.Format, maxProfileFormatBytes) ||
		!validProfileValue(target.Model, maxProfileModelBytes) || strings.ContainsRune(target.BaseURL, '?') {
		return origin{}, false
	}
	return parseOrigin(target.BaseURL, false)
}

func validProfileValue(value string, limit int) bool {
	return value != "" && len(value) <= limit && utf8.ValidString(value) &&
		strings.TrimSpace(value) == value && strings.IndexFunc(value, unicode.IsControl) < 0
}

// Formatting a registry must not disclose private binding metadata.
func (*ProfileResolver) String() string     { return "llmpolicy.ProfileResolver{[REDACTED]}" }
func (r *ProfileResolver) GoString() string { return r.String() }
