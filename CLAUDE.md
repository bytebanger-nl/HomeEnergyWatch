# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

`hwlocal` (shown to users as "Home Energy Watch"; keep `hwlocal` for the exe, service, data dir, env vars and installer AppId so upgrades keep working, and always describe HomeWizard as the devices it works with, never as the app's own brand): a single-binary Go gateway for HomeWizard Energy devices (P1, sockets, kWh meters, water meter, Plug-In Battery) that polls them over the local API, stores history in SQLite and serves a PWA. Everything stays on the LAN. The README (Dutch) is the public front page; `docs/HANDLEIDING.md` (Dutch) is the reference for configuration (`HWLOCAL_*` env vars, also flags via `hwlocal -h`), the HTTP API, deployment and TLS modes.

Language: README, docs and PWA UI text are Dutch; Go code, comments and commit messages are English.

## Commands

Requires Go 1.26+ (see `go.mod`). No C compiler needed (`modernc.org/sqlite` is pure Go), no JS build step.

The GitHub Actions workflow is turned off; these local checks are the gate before every pull request and merge (report them in the PR):

```bash
go test ./...
go vet ./... && GOOS=linux go vet ./... && GOOS=windows go vet ./...
for f in $(git ls-files '*.go'); do [ -n "$(git show HEAD:$f | gofmt -d)" ] && echo "$f"; done  # gofmt on committed content (a Windows checkout has CRLF, so plain gofmt -l lists everything)
for f in web/static/*.js; do node --check "$f"; done
go mod verify
go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...
go test -race ./...                             # needs cgo: only where a C compiler is available
```

Run against simulated devices: `scripts/dev-sim.sh [data-dir]` (default `data/dev-sim`; builds `hwsim` and `hwlocal` into `<data-dir>/bin`, binds them to 127.0.0.1 so Windows does not ask for firewall access, `scripts/dev-sim.sh stop` stops both), then open http://localhost:8080. The PWA is embedded at build time, so restart the gateway after changing `web/static`. `hwsim` serves the HomeWizard devices, fake Nord Pool/EnergyZero and tado on :18080, ista on :18081, an MHI unit on :18551, a Tuya radiator on 127.0.0.1:6668 and SMTP on 127.0.0.1:18025 (mail in `data/hwsim-mail`); `go run ./cmd/hwsim -h` lists its options. The simulated battery is API v2 only: click "Koppelen"; the simulator presses the button itself after 4 s. The P1's API v2 (port 18443, add it by hand and pair it) has `/api/batteries`, whose stand steers the simulated battery (plugin "Thuisbatterij").

Room simulation for testing automations (wait/regulate) before they touch a real air conditioner or tado: `HWSIM_ARGS="-speed 30 -room-start 17" scripts/dev-sim.sh data/kamersim`; the guide is `docs/KAMERSIMULATIE.md`, keep it in step with hwsim's options and the example rule. `-speed` only speeds up room time; the gateway's own timers stay on the clock.

Without starting the gateway: `go run ./cmd/hwlocal report -data <data-dir> [-month 2026-10] [-o file.pdf]` (PDF report) and `go run ./cmd/hwlocal prices -date 2026-11-04 [-gas] [-sources entsoe]` (ENTSO-E needs `ENTSOE_API_TOKEN`).

Cross-compile: `CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o dist/hwlocal ./cmd/hwlocal`. Linux smoke test without Linux hardware: `scripts/linux-smoke.sh [amd64] [arm64] [arm7]` (WSL on Windows, QEMU for ARM; does not cover mDNS, real-hardware speed or running as a service).

Deploy to a Windows machine running the service: `scripts/deploy-windows.sh user@host [exe]` (SSH key `~/.ssh/hwlocal_deploy` or `HWLOCAL_DEPLOY_KEY`, administrator account). It swaps the exe, restarts the service, prints the log and checks `/api/auth`, and rolls back on failure unless the database was migrated. Setup in `docs/HANDLEIDING.md` ("Bijwerken op afstand"). Deploy only builds of the default branch unless the owner asks for a branch build.

## Architecture

Data flow: `discovery` (mDNS) / manual add → `gateway.Manager` → one `worker` goroutine per device polling `homewizard.Client` every `HWLOCAL_INTERVAL` → reading goes to (a) `history.Recorder` and (b) `gateway.Hub`, which fans out JSON events to `/api/stream` (SSE: `snapshot`, then `device` / `removed`). `cmd/hwlocal/main.go` wires everything; `internal/server` holds the JSON API and serves the embedded PWA (`web/embed.go`).

Packages: `homewizard` (API v1 HTTP and v2 HTTPS + token; `normalize.go` maps v1 names to v2 so everything downstream sees one `Reading`; coarse `Kind` p1/socket/kwh/water/battery), `history` (minute samples, quarter hours, grid quality), `importer` (HomeWizard app CSV exports), `prices`, `costs` (contracts, costs, advance, scenarios, water), `rules` (alerts and automations), `webpush`, `plugin` + providers `mhi`/`tado`/`tuya` (`docs/PLUGINS.md`), `ista` (heating via mijn.ista.nl, `docs/ISTA.md`), `report` + `mail` (PDF reports, mailed on a schedule), `auth`, `tlsconfig`, `store`. `docs/CONTRACTS.md` describes the cost model.

Rules that are not visible from one file:

- **Quarter hours** are computed from cumulative counters interpolated at each 15-min boundary (`ComputeQuarters`), with quality `complete`, `partial` (a boundary estimated) or `anomalous` (a counter went backwards; that counter is nil). Minute data is pruned after `HWLOCAL_RETENTION_DAYS`; quarters are kept forever. Day/month buckets use `HWLOCAL_TZ`. Costs and energy buckets are only `complete` when every quarter is.
- **Measured vs imported quarters**: `store.ImportQuarters` only fills empty counters (never overwrites, idempotent); the recorder's measured upsert overwrites per counter and sets `source='measured'`, but only replaces an imported quarter when it is `complete`. Keep this precedence when touching either path. The importer never estimates.
- **Cost cache**: results of finished days are cached per device and day (net metering makes each day depend on the days before it in its year); `Invalidate(device, from)` drops that day and everything after it. The hooks wired in `main.go` are `Recorder.OnQuarters`, `prices.Service.OnSaved` and `Contracts.OnChange`; any new write path for quarters, prices or contracts must call one of them, or the cache goes stale. `Recorder.OnQuarters` invalidates both the electricity/gas and the water calculator.
- **Everything is priced through `costs` `quarter()`** (water: `waterQuarter`): advance, forecast, scenarios, usage costs per device, hourly billing, the rule engine's all-in prices (`Calculator.UnitPrices`) and reports. Keep new cost features on it so they follow any cost-rule change; `Calculator.Gaps` must stay in step with `computeWith` (a test compares the two). Reports never compute costs themselves.
- **User amounts are incl. VAT**; market prices get the contract's VAT. DST days have 92/100 quarters and gas days (06:00-06:00) of 23/25 hours; tests cover this.
- **Rules**: the engine evaluates every interval against `Manager.Views()`, not a worker hook. A rule fires once and re-arms only after its conditions were seen false; `armed` and running wait/regulate runs (`rule_runs`) are persisted so a restart neither repeats nor drops an action. Regulate only acts on providers with `CanRegulate` (never tado). Switching a HomeWizard socket (`Manager.SetPower`) is API v1 only.
- **Plugins**: controls are only used when the user allowed control for the instance (`_control`), and values go through `CheckValue` first. With `store.UseKey` every plugin, plugin-device and mail config is stored encrypted (`enc:v1:`) with `secret.key` in the data dir; without that key they silently load empty, so it has to move with the data dir.
- **Store**: migrations are an ordered string slice in `store.go` (`PRAGMA user_version`). Append a new entry; never edit an existing one. The database is backed up to `hwlocal.db.v<N>.bak` before migrating; a newer schema than the binary knows is refused.
- **Secrets are never returned by the API**: device tokens, the ENTSO-E token, plugin and mail passwords.
- **Security defaults** (`internal/server`): only private/loopback clients unless `HWLOCAL_ALLOW_PUBLIC` (`HWLOCAL_ALLOW_TAILSCALE` adds 100.64.0.0/10), CSRF rejected, and the `Host` header must be an IP, a LAN name or a configured name (against DNS rebinding). Server tests must set `r.Host` (httptest's default `example.com` is refused). Login is optional; `requireLogin` guards every `/api/` path but `GET /api/auth` and login/logout, and the PWA's files stay public so it can show the login screen. `auth.json` is checked before every use, so `hwlocal password` works on a running gateway. Behind `tailscale serve` everything arrives from loopback: a `Tailscale-Funnel-Request` is refused, and the login limit keys on the last `X-Forwarded-For` entry plus a shared `auth.ProxyPrefix` key (reserve attempts with `auth.Begin`/`Finish`, never check-then-count).
- **Windows service** (`service_windows.go`, stubbed by `service_other.go`; vet with `GOOS=windows`): flags are stored in the service command line because env vars do not reach the service; an installer upgrade reruns `service install` without flags so the stored ones are kept.
- **PDF reports** use the core font Helvetica: text goes through `cp1252` (the `doc` wrappers), so other characters become "?".
- **Push** (`webpush`, RFC 8291/8292, stdlib) needs a secure context: the PWA must be opened over https or localhost. The Claude browser pane cannot register service workers, so test push in a real browser.

## PWA (`web/static`)

Plain ES modules, no framework, no bundler. `app.js` owns shared state, the SSE connection, the hash router and the tab row; each other file renders one view or panel (`util.js` and `charts.js` are helpers, shared plugin UI such as `onOffSwitch` lives in `util.js`). `sw.js` caches the app shell network-first and never caches `/api/`. When adding or renaming a static file, update the `SHELL` list in `sw.js` and bump `CACHE` (`hwlocal-vN`) so installed clients pick up the change.

## Docs

`docs/GEBRUIKSHANDLEIDING.md` is the illustrated user guide (images in `docs/img/handleiding`); `docs/HANDLEIDING.md` is the reference, with known limitations at the end. Screenshots come from the simulator with demo data (`scripts/screenshots/README.md`); retake the affected ones when a screen changes visibly. When a feature changes, update the handleiding and the gebruikshandleiding, and the README when it changes what the README promises.

## Workflow

Versions: `0.1.N` where N = `git rev-list --count --first-parent` of the default branch (one per merged PR); branches build `0.1.N-dev.<sha>`. In the public repository N starts from its nearest `v0.1.K` tag. `installer/build.ps1` and `scripts/version.sh` implement the same rule; keep them in sync.

Public repository: this repository (hawkman67) is private and is where all work happens; `github.com/bytebanger-nl/HomeEnergyWatch` is the public copy, so the public name is bytebanger-nl (module path `github.com/bytebanger-nl/homeenergywatch`). `scripts/publish.sh` copies the default branch there after a merge: one public commit per first-parent commit with the same files, the PR title as message, author `bytebanger-nl`, tagged `v0.1.N`, without the private history. Never commit to the public repository directly (the script then stops); changes offered there are brought into this repository and come along with the next publish. Keep personal details (names, e-mail addresses, machine names, real data) out of the files, since every file is published. Publishing is outward-facing: run it only when the owner says so. With CI turned off there are no automatic releases: build the installer locally after a merge.

Work on a feature branch and open a pull request against the default branch, after the local checks above pass. Before opening a new PR, ask the owner whether open PRs from earlier work may be merged first. Do not merge or change the default branch without the owner. Dependabot PRs are reviewed like any other, with the same local checks.
