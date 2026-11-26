package diagnosisprep

import "errors"

// ErrCandidateBudget is shared by the protected Linux implementation and
// portable callers so cross-builds retain the same fixed failure reason.
var ErrCandidateBudget = errors.New("candidate installation budget unavailable")
