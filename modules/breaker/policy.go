package breaker

import "time"

// Rule is one ledger's policy.
type Rule struct {
	// Threshold: how many consecutive failures open the breaker.
	// **0 = never delist** (count only), used by the shape ledger.
	Threshold int
	// Cooldown: the first quarantine duration.
	Cooldown time.Duration
	// Backoff: when a half-open trial fails again, the next
	// quarantine duration is multiplied by it. 1 = no backoff.
	Backoff float64
	// MaxCooldown: the backoff ceiling.
	MaxCooldown time.Duration
}

// Policy is the whole breaker policy. The whole thing is replaceable (SetPolicy),
// so it can later be hung on state.json hot reload; for now it stays in Go,
// because this round needs the mechanism fixed first, and the knobs come later.
type Policy struct {
	Rules map[Bucket]Rule
	// TrialTTL is the lifetime of a half-open trial. If there is no conclusion this long
	// after the trial was issued (the client cancelled midway, the process was replaced),
	// the trial is considered lost and another may be let through -- otherwise an
	// unreported trial would pin the binding forever in "half-open, someone is trying".
	TrialTTL time.Duration
}

// DefaultPolicy is the factory policy.
//
// The numbers of the three ledgers are not arbitrary:
//
//   - availability 2 times / 60s: two real failures in a row basically rule out
//     flakiness. 60s is the scale of "the upstream hiccups", backing off to a 10-minute
//     ceiling, so a bad upstream is not repeatedly let back to hit user requests.
//   - rate limit 3 times / 20s: 429 recovers in seconds, and delisting it
//     with availability's 60s x 2 is too heavy -- that is what was done before
//     2026-09-17, and a rate-limited provider was delisted for 2 minutes.
//   - config 1 time / 5m / no backoff: 401/403 is a bad key, 404 is this one has no such model.
//     Both are deterministic, a second try is pure waste; backoff is meaningless too -- retrying
//     is only waiting for the user to change config, and we do not decide what to change it to.
//   - shape Threshold 0: never delist.
func DefaultPolicy() Policy {
	return Policy{
		Rules: map[Bucket]Rule{
			BucketAvailability: {
				Threshold: 2, Cooldown: 60 * time.Second,
				Backoff: 2, MaxCooldown: 10 * time.Minute,
			},
			BucketRateLimit: {
				Threshold: 3, Cooldown: 20 * time.Second,
				Backoff: 2, MaxCooldown: 2 * time.Minute,
			},
			BucketConfig: {
				Threshold: 1, Cooldown: 5 * time.Minute,
				Backoff: 1, MaxCooldown: 5 * time.Minute,
			},
			BucketShape: {Threshold: 0},
		},
		TrialTTL: 45 * time.Second,
	}
}

// rule takes one ledger's policy; if the table does not list it, treat it as "record nothing".
func (p Policy) rule(b Bucket) Rule {
	if r, ok := p.Rules[b]; ok {
		return r
	}
	return Rule{}
}

// normalized fills in zero values, so SetPolicy
// callers only write the few items they want to change.
func (p Policy) normalized() Policy {
	out := DefaultPolicy()
	if p.TrialTTL > 0 {
		out.TrialTTL = p.TrialTTL
	}
	for _, b := range []Bucket{BucketAvailability, BucketRateLimit, BucketConfig, BucketShape} {
		if r, ok := p.Rules[b]; ok {
			out.Rules[b] = r
		}
	}
	return out
}
