package verify

import (
	"context"
	"os"
	"strings"
	"sync"
	"time"

	lmfpd "github.com/Ikaleio/lm-detector/sdk/go"
	"github.com/rzbdz/newgate-ext/modules/config/domain"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// DataEnv points at a **local** reference dir (`unified_bank.json` + `shared_detector.bin`),
// for machines with no internet that also do not want a runtime download.
//
// It is also the escape hatch for "how to upgrade the bank": the SDK pulls from lm.ikale.io
// and caches, and an offline deployment can only carry data in by hand. The name is a machine
// tag, not translated; the SDK's offline mode is "put a local dir in Sources" (see its README).
const DataEnv = "NEWGATE_FINGERPRINT_DATA"

// DataTTLEnv is the bank's **network check** interval (`time.ParseDuration` form, e.g. `2h`).
//
// The bank is not push-based: nobody holds a long connection to your machine waiting for a new
// model. The policy is stale-while-revalidate - use the cache, and only past the TTL ask for
// `bundle.json` in the background, downloading 3.6 MB only if the detector sha changed.
// Default 2 hours; change this env var to check more (or less) often.
const DataTTLEnv = "NEWGATE_FINGERPRINT_TTL"

const (
	defaultDataTTL = 2 * time.Hour
	// Cooldown after a failed auto refresh: offline machines should not hit the wall every minute.
	// A manual `newgate upgrade` is not subject to it.
	refreshBackoff = time.Hour
	// How often to re-run Load within the process. It must be an order of magnitude below the
	// network TTL: a long-lived daemon that only Loads at startup would pin the bank to that
	// version forever. Re-running Load costs one cache file read, buying "once TTL expires,
	// the next verify sees new data".
	memoryTTL = time.Minute
	// The SDK's own background refresh is left to this module: its bundle timeout is only 10s
	// (see reference.go), and we have a better one. Give it a big value so Load only reads the
	// cache and does not spawn a goroutine that will fail.
	sdkRefreshDisabled = 10 * 365 * 24 * time.Hour
)

// detector is the only seam between this module and the fingerprint library.
//
// Why an interface rather than calling the SDK: web/CLI behavior (matching, caching, errors)
// must run in tests with no network and no bank. The real implementation is the one below;
// tests give a fake with fixed answers - not "for extensibility" but to be **testable**
// (same as the kernel probe running testing/upstream as a fake upstream).
type detector interface {
	// Detect runs one fingerprint round. Returns the result, the bank version, the API used.
	Detect(ctx context.Context, p domain.Provider, model, api string, count int) (*lmfpd.DetectResult, string, string, error)
	// Loaded reports whether the bank is loaded in this process, and its built_at.
	Loaded() (build string, ok bool)
	// Refresh forces a bank re-download (the `newgate upgrade` action). Returns a human result.
	Refresh(ctx context.Context) (string, error)
}

// fingerprintDetector is the real implementation: load the bank on demand, then reuse it.
//
// # Why manage another TTL layer in memory
//
// The SDK's Load checks the cache mtime **when called**: past the TTL it spawns a background
// goroutine to pull new data and still returns the old copy. But that goroutine uses the
// SDK's own 10s bundle timeout, which always fails on a slow link; and if the process only
// Loads once (as the first version did), it never gets to run. So this manages it: memory
// caches only for memoryTTL, then re-Loads; when expired, it refreshes with a **better timeout**.
type fingerprintDetector struct {
	cache *cacheStore

	mu       sync.Mutex
	det      *lmfpd.Detector
	build    string
	loadedAt time.Time
	// loading non-nil = someone is downloading/parsing the bank. **The lock is not held during
	// loading**: the UI's State() asks Loaded() every few seconds, and if that queued behind a
	// download the whole snapshot would stall (the rest of the screen has nothing to do with it).
	loading chan struct{}
	// refreshing is the single-flight marker for "background auto refresh", kept apart from
	// loading: a manual upgrade and a verify-triggered load must not be blocked by it.
	refreshing  bool
	lastAttempt time.Time
	lastErr     error
}

func newFingerprintDetector(c *cacheStore) *fingerprintDetector {
	return &fingerprintDetector{cache: c}
}

func (d *fingerprintDetector) Loaded() (string, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.build, d.det != nil
}

// dataTTL is the configured network check interval. Invalid values fall back to the default -
// this env var is an ops knob, and a typo should not disable verification entirely.
func dataTTL() time.Duration {
	if raw := strings.TrimSpace(os.Getenv(DataTTLEnv)); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil && d > 0 {
			return d
		}
	}
	return defaultDataTTL
}

// loadOptions are the options shared by every Load.
func (d *fingerprintDetector) loadOptions() lmfpd.LoadOptions {
	opts := lmfpd.LoadOptions{Cache: d.cache.dataPath(), TTL: sdkRefreshDisabled}
	// Offline deployment: someone brought the reference data directory, so read only it - no
	// network, no cache write (the SDK defines "a single non-http source" as a local dir).
	if src := strings.TrimSpace(os.Getenv(DataEnv)); src != "" {
		opts.Sources = []string{src}
	}
	return opts
}

func (d *fingerprintDetector) get(ctx context.Context) (*lmfpd.Detector, string, error) {
	d.mu.Lock()
	if d.det != nil && time.Since(d.loadedAt) < memoryTTL {
		det, build := d.det, d.build
		d.mu.Unlock()
		return det, build, nil
	}
	if ch := d.loading; ch != nil {
		// Someone is already loading: wait for it, not another 3.6 MB. Waits honor the deadline.
		d.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			return nil, "", ctx.Err()
		}
		d.mu.Lock()
		det, build, err := d.det, d.build, d.lastErr
		d.mu.Unlock()
		if det != nil {
			return det, build, nil
		}
		if err != nil {
			return nil, "", err
		}
		return nil, "", i18n.E("the fingerprint reference data could not be loaded", nil)
	}
	oldDet, oldBuild := d.det, d.build
	ch := make(chan struct{})
	d.loading, d.lastErr = ch, nil
	d.mu.Unlock()

	// The first load downloads. Give it a ceiling, but honor an earlier caller deadline: when
	// the user closes the page this download should stop.
	lctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	var det *lmfpd.Detector
	var build string
	var err error
	switch {
	case strings.TrimSpace(os.Getenv(DataEnv)) != "":
		// Local directory: no network.
		det, err = lmfpd.Load(lctx, d.loadOptions())
		if err == nil {
			build = det.BuiltAt()
		}
	case !d.cache.dataReady():
		// No cache = first run: use this module's downloader (timeout by file size).
		det, build, _, err = d.loadFresh(lctx, "")
	default:
		det, err = lmfpd.Load(lctx, d.loadOptions())
		if err == nil {
			build = det.BuiltAt()
		} else if ctx.Err() == nil {
			// Unreadable cache (corrupt, format changed): re-download it all rather than leave
			// verification stuck on a bad file. Empty knownVersion = rewrite even if same.
			det, build, _, err = d.loadFresh(lctx, "")
		}
	}

	d.mu.Lock()
	d.loading = nil
	if err != nil {
		// A failure is not recorded as permanent state: with the network back, the next verify
		// should succeed. But callers **waiting on this same load** must get the error too,
		// or they download one after another. With an old bank in hand, keep using it
		// (fail-open) - "the ruler is stale" beats "verification is unavailable".
		err = i18n.Ef(err, "cannot load the fingerprint reference data: {err}",
			i18n.A{"err": err})
		d.lastErr = err
		close(ch)
		if oldDet != nil {
			d.det, d.build = oldDet, oldBuild
			d.mu.Unlock()
			return oldDet, oldBuild, nil
		}
		d.mu.Unlock()
		return nil, "", err
	}
	d.det, d.build, d.loadedAt = det, build, time.Now()
	d.lastErr = nil
	close(ch)
	d.mu.Unlock()
	d.maybeRefresh()
	return det, build, nil
}

// loadFresh downloads, verifies and atomically writes by itself, then has the SDK read it
// once to confirm the format did not change. knownVersion is the detector sha in the
// current cache; if the remote matches, only the cache is touched and 3.6 MB is not
// downloaded again. Empty forces a re-download.
func (d *fingerprintDetector) loadFresh(ctx context.Context, knownVersion string) (*lmfpd.Detector, string, bool, error) {
	bundleBytes, detectorBytes, _, changed, err := fetchReference(ctx, knownVersion)
	if err != nil {
		return nil, "", false, err
	}
	if !changed {
		d.cache.touchData()
	} else if err := writeReferenceCache(d.cache.dataPath(), bundleBytes, detectorBytes); err != nil {
		return nil, "", false, err
	}
	det, err := lmfpd.Load(ctx, lmfpd.LoadOptions{
		Cache: d.cache.dataPath(),
		TTL:   sdkRefreshDisabled,
	})
	if err != nil {
		return nil, "", false, i18n.Ef(err, "read the refreshed fingerprint reference data: {err}",
			i18n.A{"err": err})
	}
	return det, det.BuiltAt(), changed, nil
}

// Refresh is what `newgate upgrade` calls: **force** a check and download, not wait for TTL.
//
// It lands in a temp file and replaces the old cache only if the SDK can read it; on failure
// the old file is untouched - a failed upgrade must not kill an offline machine's only ruler.
// A local data dir (DataEnv) has no download; just say so.
func (d *fingerprintDetector) Refresh(ctx context.Context) (string, error) {
	if src := strings.TrimSpace(os.Getenv(DataEnv)); src != "" {
		return i18n.T("reference data is read from the local directory {dir}; nothing to refresh",
			i18n.A{"dir": src}), nil
	}

	d.mu.Lock()
	// Wait for any in-progress load to finish, then take the "loading" slot exclusively.
	for d.loading != nil {
		ch := d.loading
		d.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			return "", ctx.Err()
		}
		d.mu.Lock()
	}
	ch := make(chan struct{})
	d.loading = ch
	d.lastAttempt = time.Now()
	oldDet, oldBuild := d.det, d.build
	d.mu.Unlock()

	// Downloads are slower than a routine Load: 3.6 MB compressed. 5 minutes, caller deadline wins.
	lctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	det, build, changed, err := d.loadFresh(lctx, d.cache.dataVersion())

	d.mu.Lock()
	defer d.mu.Unlock()
	d.loading = nil
	if err != nil {
		d.det, d.build = oldDet, oldBuild
		d.lastErr = err
		close(ch)
		return "", i18n.Ef(err, "refresh the fingerprint reference data: {err}",
			i18n.A{"err": err})
	}
	d.det, d.build, d.loadedAt = det, build, time.Now()
	d.lastErr = nil
	close(ch)
	if !changed {
		return i18n.T("fingerprint reference data is already up to date ({build})",
			i18n.A{"build": build}), nil
	}
	return i18n.T("fingerprint reference data is now {build}", i18n.A{"build": build}), nil
}

// maybeRefresh starts one background refresh when the cache is stale (stale-while-revalidate).
//
// It splits automatic updates from manual upgrade: manual upgrade blocks and reports errors;
// this only ensures "the daemon's ruler does not sit two days old", falls back to old data
// on failure, and the next verify works as usual.
func (d *fingerprintDetector) maybeRefresh() {
	if strings.TrimSpace(os.Getenv(DataEnv)) != "" {
		return
	}
	at, ok := d.cache.dataCheckedAt()
	if !ok || time.Since(at) < dataTTL() {
		return
	}
	d.mu.Lock()
	if d.refreshing || d.loading != nil || time.Since(d.lastAttempt) < refreshBackoff {
		d.mu.Unlock()
		return
	}
	d.refreshing = true
	d.lastAttempt = time.Now()
	d.mu.Unlock()

	go func() {
		defer func() {
			d.mu.Lock()
			d.refreshing = false
			d.mu.Unlock()
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		// Failure is acceptable: keep using old data and retry after the cooldown.
		_, _ = d.Refresh(ctx)
	}()
}

func (d *fingerprintDetector) Detect(ctx context.Context, p domain.Provider, model, api string, count int) (*lmfpd.DetectResult, string, string, error) {
	base, sdkAPI, apiName, err := endpointFor(p, api)
	if err != nil {
		return nil, "", "", err
	}
	det, build, err := d.get(ctx)
	if err != nil {
		return nil, "", apiName, err
	}
	if count <= 0 {
		count = 3
	}
	if count > 3 {
		count = 3
	}
	res, err := det.Detect(ctx, lmfpd.DetectConfig{
		BaseURL: base,
		APIKey:  p.Key(),
		Model:   model,
		API:     sdkAPI,
		// Count/Parallel are 1-3; one fingerprint never needs upstream parallelism above 3.
		Count:    count,
		Parallel: count,
		// A fingerprint needs the full output (292-332 integers by default). Give it time;
		// this limit only bounds "first byte / the whole JSON", not the round.
		Timeout: 120 * time.Second,
	})
	if err != nil {
		return nil, build, apiName, i18n.Ef(err, "fingerprint request failed: {err}",
			i18n.A{"err": err})
	}
	return res, build, apiName, nil
}

// endpointFor translates newgate's provider definition into the SDK's base_url + api.
//
// # The two dialects do not share one base
//
// provider.URL already spells out "the anthropic dialect uses anthropic_url, with /v1 added
// per the ANTHROPIC_BASE_URL convention" (the ark case, see domain.Provider.Base). When the
// SDK gets a base with a path it only appends /messages **after the path**; it does not add /v1.
// So this drops the trailing /messages and hands it the part that already has /v1.
//
// auto's criterion is the provider's declared protocol, not "guess who supports what":
// the protocol is a fact in the config; a wrong guess yields a conclusion unrelated to traffic.
func endpointFor(p domain.Provider, api string) (string, lmfpd.API, string, error) {
	if strings.TrimSpace(p.Key()) == "" {
		return "", "", "", i18n.E("this provider has no api key (set api_key or api_key_env): {base}",
			i18n.A{"base": p.BaseURL})
	}
	switch api {
	case "", "auto":
		if p.Protocol == "anthropic" {
			api = "messages"
		} else {
			api = "chat_completions"
		}
	}
	switch api {
	case "messages":
		base := strings.TrimSuffix(p.URL("/messages"), "/messages")
		if strings.TrimSpace(base) == "" {
			return "", "", "", i18n.E("provider has no base_url for the anthropic dialect", nil)
		}
		return base, lmfpd.APIMessages, api, nil
	case "responses":
		if strings.TrimSpace(p.BaseURL) == "" {
			return "", "", "", i18n.E("provider has no base_url", nil)
		}
		return strings.TrimRight(p.BaseURL, "/"), lmfpd.APIResponses, api, nil
	case "chat_completions":
		if strings.TrimSpace(p.BaseURL) == "" {
			return "", "", "", i18n.E("provider has no base_url", nil)
		}
		return strings.TrimRight(p.BaseURL, "/"), lmfpd.APIChatCompletions, api, nil
	default:
		return "", "", "", i18n.E("unknown fingerprint api {api} (use auto, responses, chat_completions or messages)",
			i18n.A{"api": api})
	}
}
