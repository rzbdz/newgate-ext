package webdashboard

import (
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"time"
)

// The fallback when porthub is not installed: the UI **listens on a port of its own**.
//
// How we get here: the shared port (porthub) is a module that can be removed. Remove it
// and the gateway serves the port itself, leaving the UI nowhere to stand - and this UI
// has no reason to vanish with it. So it returns to its pre-shared-port shape: one port.
//
// **It comes up only in the serving process** (see core/lib/serving): a module's Start runs
// on every `newgate …` command, so listening there means one `newgate status` opens a
// server. The criterion cannot guess argv (the kernel forbids modules reading os.Args);
// it waits for whoever owns the port to say "I am about to serve".
//
// The cost, plainly: this gives up the reason for a shared port - firewall, reverse
// proxy, ACLs per-port: one more port, one more place. It is a **fallback**, not the norm.

// FallbackPort is this path's default port (a neighbor of the gateway's default 8899).
//
// Override with the NEWGATE_WEB_PORT env var: it only means anything on this fallback,
// and a state.json field read only by a fallback weighs on every deployment.
const FallbackPort = 8898

// localhostOnly is the address this UI always binds.
//
// **Same boundary** as the shared port: that one binds loopback (the gateway's choice),
// and this cannot be wider - it too edits config and flips runtime switches (bff.go gates).
const localhostOnly = "127.0.0.1"

// standalonePort is the port number this fallback will use. `newgate web` reads it too -
// the number it tells the user must be computed in the same place as the real bind.
func standalonePort() int {
	v := os.Getenv("NEWGATE_WEB_PORT")
	if v == "" {
		return FallbackPort
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 || n > 65535 {
		// A warning nobody heeds makes people think the change applied, then suspect elsewhere.
		log.Printf("[web] %s is not a usable port number, using %d", strconv.Quote(v), FallbackPort)
		return FallbackPort
	}
	return n
}

// standaloneAddr is the address to bind this time.
func standaloneAddr() string {
	return net.JoinHostPort(localhostOnly, strconv.Itoa(standalonePort()))
}

// serveStandalone starts listening and returns the shutdown function.
//
// It mounts the UI under `/ui` - **exactly as the shared-port path does**. Not fussiness:
// front-end asset paths are fixed at build time (vite's base); another prefix is a blank
// screen, the hardest "broken" to debug. Both paths give one URL shape, one thing to learn.
func serveStandalone(handler http.Handler) (func(), error) {
	addr := standaloneAddr()
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	mux.Handle(Prefix+"/", http.StripPrefix(Prefix, handler))
	// The root points there: users first try `http://127.0.0.1:8898/`, and a 404 reads
	// as "wrong port".
	mux.Handle("/", http.RedirectHandler(Prefix+"/", http.StatusFound))

	srv := &http.Server{
		Handler: mux,
		// This UI is for the local machine only; a slow client can only be a deliberate one.
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Printf("[web] the interface stopped listening: %v", err)
		}
	}()
	// Written into the daemon's log: both share one stdout (daemon.Spawn points the log
	// file at the child's stdout), so it lands with the gateway's and `newgate logs` sees it.
	log.Printf("[web] no shared port in this build — the interface listens on http://%s%s/",
		addr, Prefix)
	return func() { _ = srv.Close() }, nil
}
