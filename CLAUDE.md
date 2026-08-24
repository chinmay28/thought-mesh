# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

Thought Mesh is an interconnected note-taking app (an Obsidian-style linked
vault, self-hosted). It is a **client-server** application: a thin browser
client over a shared backend, so every device — desktop or mobile — reads and
writes the same notes.

- `server/` — the **Go** backend: the vault (a folder of plain markdown files),
  the mesh (in-memory link index: wikilinks, backlinks, graph, search), and the
  REST API. Compiles to **one static binary** that also serves the PWA. **The
  markdown files are the single source of truth — there is no database.**
- `apps/web` (`@thoughtmesh/web`) — a mobile-friendly, installable **PWA**
  (Vite + React) that talks to the server over HTTP and behaves like an app.

There is **no auth** by design: the server runs on a trusted network
(LAN/Tailscale/VPN). This repo deliberately mirrors the conventions of its
sibling projects — CountRoster (quickstart, release workflow, UI chrome,
cloud sync) and sand-vault (calendar versioning) — when in doubt, match what
they do.

See `docs/ARCHITECTURE.md` for the full design rationale and
`docs/FEATURES.md` for the product comparison with Obsidian. **Keep both in
step with behavior changes**: a new endpoint belongs in ARCHITECTURE.md's API
table, and a feature added or removed belongs in FEATURES.md's tables (its
"known gaps" list is the closest thing to a roadmap).

## Commands

Run from the repo root:

```bash
npm install          # Node >= 20.10 (build/dev tooling for the web workspace)
npm test             # vitest (web) AND `go test ./...` (server)
npm run build        # web bundle + `go build` → server/bin/thoughtmesh
npm run typecheck    # tsc --noEmit + `go vet`
```

Go-only iteration (fast):

```bash
cd server
go test ./...        # the authoritative domain + API suites
go build ./...       # compile check
```

Run the app in development (two processes):

```bash
(cd server && go run ./cmd/thoughtmesh serve)   # API on http://localhost:8881 (flags: --vault/--port/--host)
npm run dev --workspace @thoughtmesh/web        # PWA on http://localhost:5173, proxies /api → server
```

The `serve` subcommand (also the default with no args) takes `--host`, `--port`,
`--vault`, `--web-dist`, `--cloud-settings`, `--dropbox-client-id`,
`--dropbox-client-secret`, and `--public-url` flags; each **overrides** its
env-var fallback (`HOST`, `PORT`, `THOUGHTMESH_VAULT`, `WEB_DIST`,
`THOUGHTMESH_CLOUD_SETTINGS`, `THOUGHTMESH_DROPBOX_CLIENT_ID`, …), which
overrides the built-in default. `thoughtmesh version`/`--version` prints the
version.

In production the Go binary serves the built `apps/web/dist` (embedded at build
time or via `--web-dist`) from the same origin with an SPA fallback — one
process, no CORS.

Single Go package test: `go test ./internal/mesh -run TestRename` inside
`server/`. Single web test: `npx vitest run src/lib/markdown.test.tsx` inside
`apps/web`.

There is no linter/formatter beyond `gofmt`/`go vet` (server) and TypeScript
strict mode (web).

## Architecture

```
browser PWA (apps/web)  ──HTTP/REST──>  Go server (server/)  ──>  folder of .md files
```

The client holds **no business logic** — it's a typed HTTP client
(`apps/web/src/api/client.ts`); link resolution, backlinks, rename rewriting,
search and the graph are all computed server-side.

### The files ARE the data

`internal/vault` is the note store: a directory of `.md` files, identified by
vault-relative path (`journal/2026-08-23.md`). No database, no on-disk index,
no sidecar metadata — a vault must always remain an ordinary folder that can be
synced, grepped, or opened in Obsidian without Thought Mesh in the loop. Don't
add hidden state files to the vault. Path validation is hard at this boundary
(no escaping the root, no dot-segments, no `#|[]\:*?"<>` — those break
wikilinks or synced filesystems). Writes are write-then-rename so a crash never
truncates a note; hidden entries (`.git`, `.obsidian`, `.trash`) are skipped.

### The mesh is derived, never persisted

`internal/mesh` computes link structure per request via `Snapshot()`: a
stat-walk of the vault with per-file parses cached by `(mtime, size)`. This
stays correct even when files change behind the server's back (git pull,
Syncthing, another editor) — cost is only reparsing what changed. Wikilink
syntax is Obsidian's: `[[Target]]`, `[[Target|alias]]`, `[[Target#heading]]`;
fenced code blocks don't count. A bare name resolves case-insensitively to any
note with that file name, shortest path winning ties; a target containing `/`
resolves as a path first.

**Renames go through `Mesh.Rename`**, which moves the file and rewrites the
wikilinks that pointed at it (aliases/headings preserved, code fences left
alone) — using the bare name when unambiguous after the move, the full path
otherwise. That guarantee is why renames must use the API, never a bare file
move.

### The wire contract is pinned

Snake_case JSON, integer flags, explicit `""`/`[]` over nulls, `{"error": …}`
bodies, statuses 200/201/204/400/404/409. `server/internal/api/api_test.go`
pins the contract; change it only together with `apps/web/src/api/client.ts`.
Domain errors map in `api.handleErr`: `ValidationError`→400,
`NotFoundError`→404, `ExistsError`→409. Saves take an optional `base_mtime_ms`
for optimistic concurrency — a mismatch is a 409, and the client offers "load
theirs / keep mine".

### Automatic cloud sync lives in `internal/cloud`

`server/internal/cloud` zips the whole vault (`vault.Zip` — every non-hidden
file, structure preserved) and uploads it to a Dropbox folder on a schedule
(`off|hourly|daily|weekly|monthly`), configured from the Sync page — and
restores from any `.vault.zip` snapshot in that folder. Ported from
CountRoster's cloud backup with the same three layers, and only one of them
knows a third party exists: `Provider` (OAuth + browse + upload/download),
`Service` (settings, token refresh, when the next run is due, restore),
`Scheduler` (a goroutine polling once a minute). Provider base URLs are
struct fields so tests point them at `httptest` servers.

**Restore order is load-bearing** (`Service.Restore`): download + validate
first, then write a local pre-restore backup of the vault beside the settings
file, then `vault.RestoreZip` — which itself rejects hostile archives
(traversal/absolute/hidden paths, size caps) and stages to a temp directory
before swapping, so any failure leaves the vault untouched. A restore is a
replace, not a merge; hidden entries (`.git`, `.obsidian`) survive. Don't
reorder these steps or turn restore into a merge.

Rules to preserve (mostly inherited from CountRoster):

- **Settings and tokens live in a JSON file OUTSIDE the vault**
  (`cloud.Store`, default `thoughtmesh-cloud.json` beside the vault directory,
  written 0600 and atomically). Never move this state into the vault: the
  vault is exactly what users sync/copy/version by other means, and a token
  inside it leaks with the first push. Tokens **never leave over the API**
  either — `cloud.PublicSettings` is redacted.
- **No shipped OAuth identity.** A self-hosted server has an unpredictable
  origin and providers pre-register redirect URIs, so one shipped client id
  can't serve every install. Each deployment registers its own Dropbox app;
  the client id is entered on the Sync page (stored in the settings file) with
  `--dropbox-client-id` as the fallback — the settings entry wins.
  `Provider.WithCredentials` is how the resolved pair reaches a provider, so
  never bake credentials in at construction. Changing a client id disconnects
  the account: tokens belong to the client that minted them.
- **`next_run_at` lives in the settings file**, not in a timer — that's what
  lets a server that was off over its deadline pick the run up on the next
  tick. Failed runs re-schedule on the same interval, never tight-retry.
- **Two connect modes.** Redirect (provider → `cloud.CallbackPath`, which
  bounces the browser back to `/sync?cloud=…`) and paste (`mode: "paste"` →
  no redirect URI at all; the provider shows a code the user pastes back via
  `/complete`). Paste exists because the redirect flow needs a pre-registered
  https origin a LAN server doesn't have. A code issued without a redirect URI
  **must be redeemed without one** — the pending record threads an empty
  redirect URI through to `Exchange`.

The cloud routes add two statuses to `api.handleErr`: `cloud.ConfigError`→400
(a setup gap the caller can close) and `cloud.ProviderError`→502 (the failure
came from Dropbox). `api.New` takes the service as its third argument and
skips the routes when it's nil — the web client treats a 404 on
`GET /api/cloud/sync` as "this server doesn't do cloud sync".

### The web client renders markdown itself

`apps/web/src/lib/markdown.tsx` is a deliberate hand-rolled markdown → React
renderer (no dependency): output is React elements built from text, so note
content can never inject HTML, and wikilinks render as router `<Link>`s —
resolved ones solid, missing ones dashed and leading to the create form. Keep
it dependency-free. Single newlines render as line breaks (the note-taking
convention). The editor is a plain textarea with a toolbar and a `[[`
autocomplete chip bar (see `src/components/Editor.tsx`).

### Versioning is the calendar `vYEAR.MONTH.<commit count>`

The scheme and tooling come from sand-vault (github.com/chinmay28/sand-vault):
`Year`/`Month` are consts in `server/internal/version/version.go`, bumped by
hand when a release line opens — deliberately **not** read from the build
clock, which would move the version without a commit (keep them as plain
`Year = 2026` lines — `scripts/version.mjs` parses them by regex, and rejects
a month outside 1–12). The month is never zero-padded; that keeps the tag
valid semver. The patch number is the commit count, stamped at build time
(`-ldflags -X …version.Patch=` for the binary, Vite `define` for the bundle).
An unstamped build reports patch `0`. **Don't assert the literal version
string in a test** (the shape is pinned by `internal/version/version_test.go`). The count needs the full commit
graph: `version.mjs` refuses a shallow repo, `quickstart.sh` clones with
`--filter=blob:none`, the release workflow checks out with `fetch-depth: 0` —
keep all three, and don't reintroduce `--depth 1` anywhere that feeds a build.

Pushing a `v*` tag — or a `release/v*` branch — runs
`.github/workflows/release.yml`: static `linux/<arch>` binaries (`GOARCHES` in
that file) with the PWA embedded, a `.sha256` beside each, release body from
the matching `## <tag>` section of `CHANGELOG.md`. It refuses to publish if the
tag isn't the version that commit builds. `scripts/quickstart.sh` with
`THOUGHTMESH_INSTALL=release` consumes exactly those assets — the asset names
are the contract between the two.

## Testing conventions

- Go tests live next to the code (`server/internal/**/*_test.go`). Each test
  builds a fresh vault in `t.TempDir()`.
- Web tests use vitest with globals **off**: import `describe/it/expect`
  explicitly; note the `.ts`/`.tsx` extension on relative imports.

## Conventions to preserve

- The Go server owns all HTTP; no business logic in `apps/web`.
- `CGO_ENABLED=0` must keep working — don't introduce cgo dependencies; the
  single-static-binary deploy depends on it.
- The web workspace stays ESM-only (`"type": "module"`), strict mode with
  `noUncheckedIndexedAccess` and `exactOptionalPropertyTypes`.
- The PWA service worker must **not** cache `/api` (see `apps/web/vite.config.ts`).

## Licensing & contributions

- Thought Mesh is licensed **`AGPL-3.0-only`** (`LICENSE` is the canonical GNU
  AGPL-3.0 text). Every workspace `package.json` carries
  `"license": "AGPL-3.0-only"`; keep that field on any new package.
- **Dependencies must be AGPL-compatible.** Permissive licenses
  (MIT/Apache-2.0/BSD/ISC) are fine; another copyleft license needs review; do
  not add a GPL-incompatible or proprietary dependency without flagging it.
- It's a network app, so **AGPL §13 applies to operators**: a modified server
  offered over a network must make its source available. Keep that note in
  `README.md`.
- Commit with a DCO sign-off (`git commit -s`).

## Documentation map

- `README.md` — the front door: features, getting started, install, docs index.
- `docs/ARCHITECTURE.md` — design & architecture (the "why"); its API table
  mirrors the routes in `server/internal/api`.
- `docs/FEATURES.md` — the Obsidian comparison; update it when features land
  or claims stop being true.
- `server/README.md` / `apps/web/README.md` — per-tree package maps.
- `CHANGELOG.md` — `## <tag>` sections feed release bodies verbatim.
