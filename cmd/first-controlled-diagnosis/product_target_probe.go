package main

import (
	"context"
	"errors"
	"sync"
	"time"
)

// Start another candidate while an earlier handshake is still pending. The DNS
// cap bounds concurrent workers to 32; each retains its own three-second limit.
const productTargetProbeStagger = 50 * time.Millisecond

type productTargetProbeResult struct {
	event  productTLSContact
	digest string
	until  time.Time
	err    error
}

// probeTargetPool runs only after every DNS address has passed both policies.
// The coordinator owns admission, completion audit, and winner selection. It
// cancels and drains every admitted worker before returning any connection.
func (r *productTargetRegistry) probeTargetPool(ctx context.Context, stage string, rec productTargetRecord, host string, addresses []string, jobID string) (string, string, error) {
	if len(addresses) < 1 || len(addresses) > productMaxTargetAddresses {
		return "", "", errPreparation
	}
	workCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var workers sync.WaitGroup
	results := make(chan productTargetProbeResult, len(addresses))
	timer := time.NewTimer(0)
	defer timer.Stop()
	next, active := 0, 0
	var winner *productTargetProbeResult
	var failure error
	auditBroken, expired := false, false
	plain := productTargetPlain(rec.Target)
	for next < len(addresses) || active > 0 {
		// No admission after a winner or a fatal failure. Cancellation does not
		// detach workers: collect all results, including late audit failures.
		var result productTargetProbeResult
		haveResult := false
		if winner != nil || failure != nil {
			if active == 0 {
				break
			}
			result = <-results
			haveResult = true
		} else {
			// Prefer already completed probes over starting another candidate.
			select {
			case result = <-results:
				haveResult = true
			default:
			}
			if !haveResult {
				select {
				case <-ctx.Done():
					failure = ctx.Err()
					cancel()
					continue
				case result = <-results:
					haveResult = true
				case <-timer.C:
					if err := ctx.Err(); err != nil {
						failure = err
						cancel()
						continue
					}
					if stage == "start" && r.current(rec) != nil {
						failure = errors.New("target_approval_changed")
						cancel()
						continue
					}
					event, err := r.beginTargetContact(ctx, stage, rec, host, addresses[next], jobID)
					if err != nil {
						failure = err
						auditBroken = errors.Is(err, errPreparation)
						cancel()
						continue
					}
					next++
					active++
					workers.Add(1)
					go func(event productTLSContact) {
						defer workers.Done()
						result := productTargetProbeResult{event: event}
						probeCtx, stop := context.WithTimeout(workCtx, productTargetProbeTimeout)
						defer stop()
						defer func() { results <- result }()
						if err := probeCtx.Err(); err != nil {
							result.err = err
							return
						}
						if plain {
							result.err = r.reach(probeCtx, event.IP, event.Port)
						} else {
							result.digest, result.until, result.err = r.probe(probeCtx, event.Host, event.IP, event.Port)
						}
						if result.err == nil {
							result.err = probeCtx.Err()
						}
					}(event)
					if next < len(addresses) {
						timer.Reset(productTargetProbeStagger)
					}
				}
			}
		}
		if !haveResult {
			continue
		}
		active--
		if auditBroken {
			continue
		}
		probeErr, auditErr := r.completeTargetContact(result.event, plain, result.digest, result.until, result.err)
		if auditErr != nil {
			auditBroken = true
			failure = auditErr
			cancel()
			continue
		}
		if errors.Is(probeErr, errPreparation) {
			failure = errPreparation
			cancel()
			continue
		}
		if failure != nil || winner != nil {
			continue
		}
		if err := ctx.Err(); err != nil {
			failure = err
			cancel()
			continue
		}
		if stage == "start" && r.current(rec) != nil {
			failure = errors.New("target_approval_changed")
			cancel()
			continue
		}
		if probeErr == nil {
			winner = &result
			cancel()
		} else {
			expired = expired || errors.Is(probeErr, errTargetCertificateExpired)
			if active == 0 && next < len(addresses) {
				timer.Reset(0)
			}
		}
	}
	workers.Wait()
	if failure != nil {
		return "", "", failure
	}
	if err := ctx.Err(); err != nil {
		return "", "", err
	}
	if stage == "start" && r.current(rec) != nil {
		return "", "", errors.New("target_approval_changed")
	}
	if winner != nil {
		if !plain && !winner.until.After(r.now()) {
			return "", "", errTargetCertificateExpired
		}
		return winner.event.IP, winner.digest, nil
	}
	if plain {
		return "", "", errors.New("target_unreachable")
	}
	if stage == "approve" && expired {
		return "", "", errTargetCertificateExpired
	}
	return "", "", errors.New("target_tls_unverified")
}
