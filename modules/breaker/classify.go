package breaker

import i18n "github.com/rzbdz/newgate/lib/i18n"

// Kind is the classification of "what actually happened to this
// upstream request", built by the caller from what really happened.
//
// There are only **facts** here, no judgment: the data plane says "the connection
// failed", "the upstream returned 500", "bytes have already been written to the
// client", without interpreting what that means. Interpretation is in Classify.
type Kind int

const (
	// KindUpstreamSuccess: the upstream returned a response
	// normally (including 3xx: the client follows it itself).
	KindUpstreamSuccess Kind = iota
	// KindConnError: a dial/DNS/TLS/reset/EOF failure, or a time-to-first-byte timeout.
	KindConnError
	// KindClientCancel: the client cancelled itself. Unrelated to the upstream; charged to nobody.
	KindClientCancel
	// KindUpstreamStatus: the upstream returned a status code >= 400.
	KindUpstreamStatus
	// KindStreamTruncated: the first byte had arrived and bytes had started flowing
	// to the client, and the upstream broke mid-stream. Cannot switch hops (what
	// is written out cannot be taken back), but it is a real availability problem.
	KindStreamTruncated
)

// Bucket is which ledger this failure should be charged to. Different ledgers
// have different thresholds and cooldowns -- lumping them into one "delist
// after two consecutive failures" was the core flaw before 2026-09-17:
//
//   - availability: connection failure/timeout/5xx/stream truncation, really "this one is down now";
//   - rate limit: 429 recovers in seconds, and delisting it with 60s x 2 backoff is too heavy;
//   - config: 401/403/404 are deterministic, delist on the first one, no backoff
//     needed (retrying only waits for the user to fix config or switch model);
//   - shape: a request-shape error (the same body is equally wrong whoever sends it)
//     has nothing to do with "can this one be used", so **never delist**, only count.
type Bucket int

const (
	BucketNone Bucket = iota
	BucketAvailability
	BucketRateLimit
	BucketConfig
	BucketShape
)

// ruleToken is this ledger's name on the wire and on disk: `Status.Rule`, `health.json`.
//
// It is a **machine marker**, so it is not translated (see the package comment of lib/i18n and
// the forbidden list in tools/i18n/check): this value is read back by persist.bucketFromName
// for reconciliation, and once it changed with the language, a health.json written
// under zh would be unrecognizable under en -- what is stored is data, not a sentence.
func (b Bucket) ruleToken() string {
	switch b {
	case BucketAvailability:
		return "availability"
	case BucketRateLimit:
		return "rate limit"
	case BucketConfig:
		return "config"
	case BucketShape:
		return "request shape"
	}
	return ""
}

// ruleName is the human-facing name: the "ledger" column
// of the table, and the sentence inside a delisting reason.
func (b Bucket) ruleName() string {
	switch b {
	case BucketAvailability:
		return i18n.T("Availability", nil)
	case BucketRateLimit:
		return i18n.T("Rate limit", nil)
	case BucketConfig:
		return i18n.T("Config", nil)
	case BucketShape:
		return i18n.T("Request shape", nil)
	}
	return ""
}

// Input is all the facts classification needs. **No hidden inputs**:
// Shape is computed beforehand by Report and put in, so Classify is
// a pure function that can be exhaustively tested with one table.
type Input struct {
	Kind   Kind
	Status int
	// Body is the upstream's raw error text (already truncated).
	// Only Report reads it -- to ask the shape detectors.
	Body []byte
	// Written: have bytes already been written to the client? If so,
	// cannot switch hops: the client has already received half a response,
	// and having someone else continue writing just yields spliced garbage.
	Written bool
	// IsLast: is there another hop on the chain.
	IsLast bool
	// FallbackOn400 is the chain.fallback_on_400 switch.
	FallbackOn400 bool

	// Shape: a registered shape detector matched (filled by Report; Classify only reads it).
	Shape bool
}

// Verdict is the classification's verdict. The data plane decides from it
// whether to "keep going along the chain" or "hand this round to the client".
//
// No Record field: `Bucket == BucketNone` means "do not record". Two
// fields expressing the same thing will inevitably drift -- keep one.
type Verdict struct {
	// Advance: switch to the next hop.
	Advance bool
	// Bucket: which ledger it goes to; BucketNone means record nothing (success, client
	// cancel, and the 4xx of "the request itself is bad, so it is equally wrong anywhere").
	Bucket Bucket
}

// Result is Report's return: the verdict + side effects
// produced by this recording (for logging only).
type Result struct {
	Verdict
	// Opened: this failure tripped the breaker.
	Opened bool
	// Spared: consecutive failures reached the threshold, but the
	// **diagnostic probe before tripping** proved this binding still usable,
	// so it was not delisted and the count was reset. See Breaker.SetVerifier.
	Spared bool
	// Shape is the name of the detector that claimed this 400 (non-empty when BucketShape).
	//
	// The data plane uses it for dedicated logging and evidence saving, **while staying ignorant of
	// the upstream's proprietary strings**: the name is chosen by the detector itself ("deepseek"),
	// and nowhere in core does "must be passed back" appear. Before 2026-09-17, forward
	// itself recognized those two sentences (the function was then called IsRequestShapeError
	// and lived in core), which copied the upstream dialect into the forwarding path.
	Shape string
}

// Classify is the **single** failure classification table. Pure function: same Input, same Verdict.
//
// This table exhausts every way an upstream interaction on the data plane can end. The reasons:
//
//	connection failure/TOFB timeout  -> advance + record availability: a clear "down now"
//	stream broken (bytes written)     -> no switch (cannot unsend) + record availability
//	client cancel                     -> do nothing: not upstream's fault, no point going on
//	400 + shape hit                   -> **advance** + count only: same body is equally
//	                                     wrong whoever sends it, so not charged here; but a
//	                                     looser-validating provider may accept it, so keep
//	                                     trying. **Not** subject to fallback_on_400: that
//	                                     stops ramming bad requests everywhere, while a
//	429                               -> advance + record rate limit
//	401/402/403/404                   -> advance + record config (402 insufficient balance is a
//	                                     deterministic config problem: this one is out of money,
//	                                     so switch; that is the only direction that can succeed)
//	408/409/5xx                       -> advance + record availability
//	other 4xx (incl. non-shape 400)   -> record nothing, switch nothing: the request is bad
//	3xx / 2xx                         -> do nothing (the success path is KindUpstreamSuccess)
//
// No chain-budget exhaustion here: that is not an upstream outcome; the data plane sets
// IsLast -- with no budget only the current hop is tried, and a failure is still recorded.
func Classify(in Input) Verdict {
	// Bytes have already been written to the client: under no circumstances can the hop change.
	advance := !in.Written && !in.IsLast

	switch in.Kind {
	case KindUpstreamSuccess:
		return Verdict{}
	case KindClientCancel:
		// The client left on its own; nobody is there to receive. Retrying along the chain would
		// only burn someone else's money, and even if it retried nobody could receive the result.
		return Verdict{}
	case KindConnError:
		return Verdict{Advance: advance, Bucket: BucketAvailability}
	case KindStreamTruncated:
		return Verdict{Advance: false, Bucket: BucketAvailability}
	}

	// KindUpstreamStatus
	switch {
	case in.Status == 400 && in.Shape:
		return Verdict{Advance: advance, Bucket: BucketShape}
	case in.Status == 429:
		return Verdict{Advance: advance, Bucket: BucketRateLimit}
	case in.Status == 401, in.Status == 402, in.Status == 403, in.Status == 404:
		return Verdict{Advance: advance, Bucket: BucketConfig}
	case in.Status == 408, in.Status == 409, in.Status >= 500:
		return Verdict{Advance: advance, Bucket: BucketAvailability}
	case in.Status == 400 && in.FallbackOn400:
		// The user explicitly asked to try further down even on 400. Still record nothing
		// -- a problem with the request itself is not an upstream availability problem.
		return Verdict{Advance: advance}
	}
	// Other 4xx: the request itself is bad. Going further
	// means ramming a bad request into every upstream.
	return Verdict{}
}
