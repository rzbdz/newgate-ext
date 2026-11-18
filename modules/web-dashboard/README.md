# web-dashboard

Browser UI + its BFF. Mounted under the `/ui` prefix on the gateway port (default 8899).

## Three things, each with an owner

| Who | Owns what |
| --- | --- |
| `modules/porthub` | The owner of that port. This module only does `Mount("/ui", ...)` |
| This module's `bff.go` | Four things: serve the concept ledger, hand edits to the concept's owner, forward extension endpoints, serve static assets. **Knows no module** |
| The business modules | What their own surface looks like and how to write back to it (`lib/view`, Optional dependency) |

Adding a module's UI = that module calls `Register` in its own `Start`; neither this package nor the frontend changes
(the frontend only knows Kind, see `web/src/api.ts`).

### The second door: `/api/ext/<name>`

The concept ledger only holds things that **fit on one card**. Some features are not a card: for example, "open the model dropdown,
ask what models this upstream offers right now": it has no persistable shape, and it should not touch
the network on every snapshot. In that case the module optionally depends on this module's `web-extension` port and, in its own `Start`,
registers **a name + a function**:

```go
rel, err := web.Register("modellist", func(r webapi.ExtensionRequest) (any, error) {
    // The shape is yours; the BFF hands you the request body as-is and writes your return value as JSON.
    return myAnswer, nil
})
```

The frontend does `POST /ui/api/ext/<name>` with a JSON body. The BFF only does four things of a formal nature: accept POST,
accept `application/json`, find the name, forward--it still knows no module. An unregistered extension returns 404
(the UI falls back to the data it can get), and when an extension returns an error the BFF returns 400 and passes that sentence
through to the UI unchanged. Today's first user is `modules/modellist` (the list source for the model picker).

### The third door: a module brings its own frontend (plugins)

The first two doors both require the **frontend to know the shape in advance** (a Kind, or some extension endpoint's answer). When a module wants
a piece of UI the frontend does not know, it should not add components to `web/src`; instead it **brings its own frontend code**:

```go
//go:embed web
var webFiles embed.FS

plugins.RegisterPlugin(webapi.Plugin{
    Name:   "self-update",
    Assets: sub(webFiles, "web"),   // the module's own ES module + static files
    Entry:  "plugin.js",            // export function mount(el, host) { …; return unmount }
    Slots:  []webapi.PluginSlot{webapi.SlotTopbar},
})
```

The BFF serves the list at `/api/plugins` and the files at `/plugins/<name>/...`; the UI (`PluginSlot.svelte`)
gives each plugin an empty container and a host in a **slot** of its own layout (`ext()` calls an extension endpoint, `asset()`,
`lang`). The UI knows only slots and this contract; a plugin's copy comes from its own extension endpoint (translated on the backend),
styles use design tokens. A plugin that fails to mount loses only its own cell. Today the only slot is `topbar`; if you need another
(a sidebar section, a full page), add a slot here--that is a **customization point with more freedom** for modules, not a way to make
the UI know about modules. The first user is `modules/self-update` (the top bar's "Update").

### Layering: UI <- wrapper layer <- modules

- **The UI (this module)** provides only framework interfaces: Kind, slots, the plugin contract, extension endpoint forwarding. It knows
  no module.
- **A single module's** UI: it brings its own plugin (the point above).
- **The UI assembled from several modules** (the model dropdown needs modellist's list + verify's fingerprints): do not put it
  into the UI, and do not make one business module depend on another--put it in a **wrapper module**, which treats those modules as
  data upstreams (Optional dependencies on their ports) and injects the aggregate into the UI in one go. When the level of abstraction is unclear,
  add another layer instead of letting the UI overstep.

### Who crossed the line (2026-10-29 survey, to be migrated)

The ratchet in `boundary_test.go` guards "frontend source must not hardcode extension endpoint names"; the first two rows below are on its list
(**it may only shrink**); the rest it cannot detect, and they are listed here:

| Where | Which line it crossed | Where it should go |
| --- | --- | --- |
| `kinds/Fingerprints.svelte` + the fingerprint types in `api.ts` and `/ext/fingerprint` | the whole fingerprint page is modules/verify's UI | verify brings its own plugin (needs a "full page" slot) |
| `ModelPicker.svelte` asks `/ext/modellist` and `/ext/fingerprint` directly | the model dropdown knows two modules | the UI defines a "model source" port; a wrapper module aggregates modellist + verify to implement it |
| kernel `lib/view`'s `KindFingerprints` | the kernel knows the shape of a distribution module | delete it once the fingerprint page moves to a plugin |
| `kinds/Overview.svelte` branches on the action id prefix `probe` | knows how home's actions are named | actions declare semantic fields like "how long it takes / whether to spin" themselves |
| `App.svelte`'s top bar only renders the section action with id `fallback` | knows one module's action id | actions declare "goes in the top bar" (a placement field), and the UI places them by that field |

## Local debugging: how to open it in a browser

**Do not touch the live daemon** (it runs on 8899, and your own requests flow through it too). Start a sandbox
instance on a different port:

```bash
# 1. a minimal config (put it elsewhere; do not touch ~/.config/newgate)
mkdir -p /tmp/ngui/home/mappings
cat > /tmp/ngui/home/providers.json <<'JSON'
{"providers":{"demo":{"protocol":"anthropic","base_url":"http://127.0.0.1:9/demo","api_key":"sk-x"}}}
JSON
cat > /tmp/ngui/home/mappings/demo.json <<'JSON'
{"description":"debug profile","roles":{"normal":[{"provider":"demo","model":"demo-model"}]}}
JSON

# 2. build an artifact, and **drop a copy under the right name** (argv0 decides who this call belongs to)
build/build.sh
mkdir -p /tmp/ngui/bin && cp dist/newgate-default-linux-amd64 /tmp/ngui/bin/newgate

# 3. start it
cd /tmp/ngui && NEWGATE_HOME=/tmp/ngui/home HOME=/tmp/ngui ./bin/newgate __serve --port 8901
```

Then open <http://127.0.0.1:8901/ui/>. The `api_key` in `providers.json` should show as
`***` in the UI, and that file should be **read-only**.

Changing the frontend: `cd modules/web-dashboard/web && pnpm install && pnpm dev` (vite's dev
server starts on 5173 by itself; proxy `/ui/api` to the 8901 above for hot reload), then
`pnpm build`--**the build output must be committed** (Go's `go:embed` reads it, which is what lets `go build` work offline).

## Assembly: the entry has three shapes, and `newgate web` tells you which one this is

**Two independent axes**; do not mix them: one is "who is the terminal UI" (`cli` / `tui` / `simple-cli`),
the other is **where this UI is served from**:

| porthub | serving | where the UI is | what this assembly looks like |
| --- | --- | --- | --- |
| yes | any | shared port `/ui` (the same port as the data plane) | normal |
| no | yes | **its own port** `http://127.0.0.1:8898/ui/` (`NEWGATE_WEB_PORT` can change it) | fallback |
| no | no | no entry | `newgate web` explains why |

The fallback only takes effect in the **serving process** (registered in `lib/serving`'s book and announced by whoever owns the port
when it starts serving)--in a CLI process it never opens a listener. It gives up exactly the reason we wanted a shared port
(firewalls / reverse proxies / ACLs are all configured per port), so it is a fallback, not the norm.

The terminal UI axis is unrelated to it and stands on its own: the default (`cli`/`tui` + this module) gives the terminal and the browser
one each; with the kernel's `cli` turned off the browser keeps working and the terminal falls back to minimal rendering (`simple-cli` steps up);
**without this module** there is no web entry at all, and `/ui` falls into the data plane's catch-all (like any unknown path
does). All these combinations hold because this module's dependencies are **all `Optional`**.

**In an assembly with no terminal UI, the browser is the only entry** (`dist-dashboard.json` is exactly one of those:
it turns off the kernel's `cli`). Then:

```bash
newgate --port 8899      # no subcommand = start serving (only this entry's own flags are not claimed by anything else)
```

This did not hold before: `__serve` used to be a command of `cli`, so an assembly with `cli` off would build
but not start (`no entry claimed this call`). Since 2026-09-20 the daemon itself is an **entry
declaration** (`component/entry`), independent of whether a UI is installed; the same change also made "no arguments = start serving"
hold **with no terminal UI**--then there is no subcommand to dispatch, and the only sensible behavior of this binary is to
serve. Other arguments (`newgate frobnicate`, `newgate --help`) still get the plain-English "no entry claimed
this call".

## Who can touch it

It **can change config and flip runtime switches**, so two gates are hard (both in `bff.go`, each with comments):

1. **Answer only the local Host** (127.0.0.1 / localhost / ::1). Listening on loopback only blocks outside
   connections; it does not block a script on someone else's web page: the attacker resolves a domain they control to 127.0.0.1
   (DNS rebinding) and the browser then considers it same-origin. The only thing the browser does not lie about is the Host it fills in.
   Cost: people who give themselves a name in `/etc/hosts` are locked out, and the error says which address to use.
2. **Write operations must declare `Content-Type: application/json`**. Cross-site forms and no-cors
   fetch cannot send this type (it triggers a preflight, and we send no CORS headers), but they can send a text/plain body
   that looks like JSON--and the decoder does not look at Content-Type. This is the only wall on that road.

**Residual risk, stated plainly**: **any process** on this machine can still read and write these endpoints directly (the loopback boundary
is this machine). So treat it as being at the same level as `providers.json`--credentials are masked there, and files carrying credentials
are read-only in the UI, both for this same reason.

## TODO

- **Opening the browser**: `newgate web` only prints the address; it does not run `xdg-open` for the user (that kind of call
  hangs on jump hosts). Adding it would first require thinking through the behavior "when there is no graphical interface".
- **Time limits for footguns**: among the switch dots in the UI, `DangerFootgun` ones are put on a **read-only**
  card (writing them requires a time limit, and this version has no UI for choosing one). Today no module declares a footgun,
  so this is paving the way for the future--it cannot be validated with real data now.
- **`gateway.special` cannot be written yet**: it is read-only, and **in an assembly with cli off
  `newgate st on|off` does not exist either**--there, to toggle a patch you can only go to the code card
  of `config.file.state.json` and edit `ModuleConfig["gateway"]` by hand. Adding it needs a CAS write
  (`store.WriteIfUnchanged`, turning `StaleError` into `view.Conflict`), the same kind of work as the footgun item.
  Today it can be validated with real data (six patches are registered in this assembly).
- **Non-loopback exposure**: right now it answers only this machine. To open it to others you first need authentication (the control token is
  half of what is needed), see the route in `docs/12-newgate-remote.md`.

## What cards exist today

| Concept ID | Kind | Contributed by | Writable |
| --- | --- | --- | --- |
| `config.file.*` | `code` | config | `providers.json` read-only (credentials), the rest writable |
| `config.profile.*` | `mapping-editor` | config | yes |
| `config.state` | `toggles` | config | yes |
| `gateway.metrics` | `series` | gateway | read-only |
| `gateway.log` | `log` | gateway | read-only |
| `gateway.special` | `table` | gateway | read-only: **which patches are touching your requests** (the two kinds of "off" each get their own wording and color; in an assembly with cli off this is the only answer) |
| `claudecode.classifier` | `table` | claudecode | read-only: **how the Bash classifier is being treated right now** (unguarded and off / time-limited / permanent; only permanent is drawn red). Also the only place to see it in an assembly with cli off |
| `plugin-manager.switches` | `toggles` | plugin-manager | yes |
| `plugin-manager.footguns` | `toggles` | plugin-manager | read-only (needs a time limit, see above) |
| `plugin-manager.modules` | `table` | plugin-manager | read-only: **which modules make up this build** |
| `breaker.health` | `table` | breaker | read-only |
| `runtime.takeover` | `table` | runtime | read-only: **who is going through newgate** (four states: active / declared but not active / turned off but not released / direct) |
| `opencode-omo.slots` | `table` | opencode-omo | read-only: the current binding of each omo slot (write ownership has a single semantics: `newgate omo use`) |

This table is a **product**, not a checklist: adding a module's surface does not require changing it, just as it does not require changing the frontend
(the frontend only knows Kind; the ledger only knows registrations). It is written here so you can see at a glance "what can be seen today";
do not treat it as a list that needs maintaining.

The `table` ones (six today) were added on 2026-09-20: the shape is defined in `lib/view` (columns + rows, with rows indexed by **column ID**
rather than array position, so column order is purely a display matter), and the frontend's `kinds/Table.svelte` recognizes it.
The second contributor (module list) did not change a single line of frontend code--that is exactly the reason to make it a contract
rather than a one-off renderer.
