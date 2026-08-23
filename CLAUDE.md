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
sibling project CountRoster (versioning, quickstart, release workflow, UI
chrome) — when in doubt, match what CountRoster does.

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
(cd server && go run ./cmd/thoughtmesh serve)   # API on http://localhost:8788 (flags: --vault/--port/--host)
npm run dev --workspace @thoughtmesh/web        # PWA on http://localhost:5173, proxies /api → server
```

The `serve` subcommand (also the default with no args) takes `--host`, `--port`,
`--vault`, and `--web-dist` flags; each **overrides** its env-var fallback
(`HOST`, `PORT`, `THOUGHTMESH_VAULT`, `WEB_DIST`), which overrides the built-in
default. `thoughtmesh version`/`--version` prints the version.

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

### The web client renders markdown itself

`apps/web/src/lib/markdown.tsx` is a deliberate hand-rolled markdown → React
renderer (no dependency): output is React elements built from text, so note
content can never inject HTML, and wikilinks render as router `<Link>`s —
resolved ones solid, missing ones dashed and leading to the create form. Keep
it dependency-free. Single newlines render as line breaks (the note-taking
convention). The editor is a plain textarea with a toolbar and a `[[`
autocomplete chip bar (see `src/components/Editor.tsx`).

### Versioning is `vMAJOR.MINOR.<commit count>`

Identical scheme and tooling to CountRoster: `Major`/`Minor` are consts in
`server/internal/version/version.go` (keep them as plain `Major = 0` lines —
`scripts/version.mjs` parses them by regex); the patch number is the commit
count, stamped at build time (`-ldflags -X …version.Patch=` for the binary,
Vite `define` for the bundle). An unstamped build reports patch `0`. **Don't
assert the literal version string in a test.** The count needs the full commit
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
