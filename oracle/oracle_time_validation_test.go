package oracle

import (
	"testing"
	"time"
)

func timePolicyCases() []struct {
	name   string
	access ExpectedAccess
	want   VerdictValue
} {
	return []struct {
		name   string
		access ExpectedAccess
		want   VerdictValue
	}{
		{"deny", AccessDeny, Confirmed},
		{"allow", AccessAllow, Allowed},
		{"unknown", AccessUnknown, Suspected},
	}
}

func TestOracleInvalidTimesAreUndecidable(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*CaseInput, *time.Time)
	}{
		{"missing_observation", func(_ *CaseInput, at *time.Time) { *at = time.Time{} }},
		{"missing_start", func(c *CaseInput, _ *time.Time) { c.Policy.ValidFrom = time.Time{} }},
		{"missing_end", func(c *CaseInput, _ *time.Time) { c.Policy.ValidUntil = time.Time{} }},
		{"missing_observation_and_start", func(c *CaseInput, at *time.Time) {
			c.Policy.ValidFrom, *at = time.Time{}, time.Time{}
		}},
		{"observation_year_zero", func(c *CaseInput, at *time.Time) {
			*at = time.Date(0, time.June, 1, 0, 0, 0, 0, time.UTC)
			c.Policy.ValidFrom, c.Policy.ValidUntil = at.Add(-time.Hour), at.Add(time.Hour)
		}},
		{"observation_year_10000", func(c *CaseInput, at *time.Time) {
			*at = time.Date(10000, time.June, 1, 0, 0, 0, 0, time.UTC)
			c.Policy.ValidFrom, c.Policy.ValidUntil = at.Add(-time.Hour), at.Add(time.Hour)
		}},
		{"start_year_zero", func(c *CaseInput, _ *time.Time) {
			c.Policy.ValidFrom = time.Date(0, time.June, 1, 0, 0, 0, 0, time.UTC)
		}},
		{"end_year_10000", func(c *CaseInput, _ *time.Time) {
			c.Policy.ValidUntil = time.Date(10000, time.June, 1, 0, 0, 0, 0, time.UTC)
		}},
	}
	for _, path := range timePolicyCases() {
		t.Run(path.name, func(t *testing.T) {
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					input, at := baseCase(), observedAt
					input.Policy.ExpectedAccess = path.access
					tc.mutate(&input, &at)
					got := Evaluate(input, at)
					if got.Value != Undecidable || got.PolicyCheck != StateUnknown ||
						len(got.ReasonCodes) != 3 || got.ReasonCodes[0] != ReasonExpectedAccessPolicyInvalid {
						t.Fatalf("invalid time gave verdict %+v", got)
					}
				})
			}
		})
	}
}

func TestOracleValidHistoricalTimesUseUTCInstants(t *testing.T) {
	cases := []struct {
		name                  string
		from, observed, until time.Time
	}{
		{"current", observedAt.Add(-time.Hour), observedAt, observedAt.Add(time.Hour)},
		// The displayed local year is zero, but the represented UTC instant is
		// in year one and nonzero. Validate the instant after normalization.
		{"utc_year_one", time.Date(1, 1, 1, 0, 1, 0, 0, time.UTC),
			time.Date(1, 1, 1, 1, 0, 0, 0, time.UTC).In(time.FixedZone("local-year-zero", -12*3600)),
			time.Date(1, 1, 1, 2, 0, 0, 0, time.UTC)},
		{"utc_year_9999", time.Date(9999, 12, 31, 21, 0, 0, 0, time.UTC),
			time.Date(9999, 12, 31, 22, 0, 0, 0, time.UTC),
			time.Date(9999, 12, 31, 23, 0, 0, 0, time.UTC)},
	}
	for _, path := range timePolicyCases() {
		t.Run(path.name, func(t *testing.T) {
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					input := baseCase()
					input.Policy.ExpectedAccess = path.access
					input.Policy.ValidFrom, input.Policy.ValidUntil = tc.from, tc.until
					got := Evaluate(input, tc.observed)
					if got.Value != path.want {
						t.Fatalf("valid historical time gave verdict %+v, want %s", got, path.want)
					}
				})
			}
		})
	}
}
