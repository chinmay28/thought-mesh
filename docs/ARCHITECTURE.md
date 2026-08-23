# Thought Mesh — Design & Architecture

This is the reasoning document: what the system is, how the pieces fit, and
why the decisions that are meant to last were made. `CLAUDE.md` at the repo
root is the working summary for day-to-day changes; `docs/FEATURES.md`
compares the product against Obsidian.

## 1. What it is

An interconnected note-taking app — the Obsidian model (a *vault* of markdown
files joined by `[[wikilinks]]`) rebuilt as a **client-server** app so every
device shares one vault. A small Go server owns a folder of `.md` files and
serves both a REST API and an installable PWA from one origin.

```
┌─────────────┐   HTTP/REST    ┌──────────────┐   os.ReadFile/WriteFile
│ browser PWA │ ─────────────> │  Go server   │ ────────────────────────> vault/*.md
│  (apps/web) │ <───────────── │  (server/)   │ ──── zip on schedule ───> Dropbox
└─────────────┘                └──────────────┘
```

The deployable artifact is **one static Go binary** (`CGO_ENABLED=0`) with the
built web client embedded: no runtime dependencies, one process, one origin,
no CORS. Node exists only at build time, to compile the PWA with Vite.

### Repository layout

```
thought-mesh/
├── server/                     # the Go backend
│   ├── cmd/thoughtmesh/        #   CLI + composition root; embeds webdist/
│   └── internal/
│       ├── vault/              #   the note store: a folder of .md files
│       ├── mesh/               #   derived link structure: backlinks, graph, search
│       ├── cloud/              #   automatic Dropbox sync
│       ├── api/                #   the HTTP layer (wire contract pinned by tests)
│       └── version/            #   vYEAR.MONTH.<commit count>
├── apps/web/                   # the PWA client (Vite + React)
├── scripts/                    #   version.mjs, quickstart.sh
├── deploy/                     #   reference systemd unit
└── docs/                       #   this document, FEATURES.md
```

## 2. The files are the data

The single most load-bearing decision: **the vault is a plain folder of
markdown files, and nothing else.**

- No database. No on-disk index. No sidecar metadata, ever.
- A vault must stay openable in Obsidian, greppable, syncable (git, Syncthing,
  rsync), and portable — with Thought Mesh entirely out of the loop.
- Consequence: everything derived (backlinks, the graph, search results) is
  computed in memory and can always be recomputed from the files alone.

This is why the server tolerates the vault changing behind its back. The mesh
index (`internal/mesh`) is rebuilt per request from a stat-walk, with per-file
parse results cached by `(mtime, size)` — correctness first, and the cost of a
request is only reparsing files that actually changed. For vaults in the
thousands of notes this is comfortably fast; if it ever isn't, the fix is a
filesystem watcher feeding the same cache, not persistence.

### The vault store (`internal/vault`)

- A note is identified by its vault-relative path with forward slashes, always
  ending `.md` (`journal/2026-08-23.md`). `Name` is the file stem; `Dir` the
  parent folder.
- Paths are validated hard at this boundary: no escaping the root, no `.`/`..`
  or dot-prefixed segments (which also keeps `.git`, `.obsidian`, `.trash`
  invisible), none of `# | [ ] \ : * ? " < >` — the first four break wikilink
  syntax, the rest break Windows and common sync targets. Control characters
  and over-long segments are rejected.
- Writes are write-to-temp-then-rename, so a crash can never truncate a note.
- Deletes and renames prune emptied parent folders.
- `Zip()` snapshots the whole vault (every non-hidden file, not just `.md`,
  structure preserved) — the artifact cloud sync uploads.

## 3. Links and the mesh (`internal/mesh`)

Wikilink syntax is Obsidian's: `[[Target]]`, `[[Target|shown text]]`,
`[[Target#heading]]`. Parsing skips fenced code blocks. Resolution:

1. A target containing `/` is tried as a vault path (case-insensitive), then
   falls back to its final segment as a name.
2. A bare name matches any note with that file name, case-insensitively.
   Shortest path wins a tie (the "closest to the root" note is almost always
   the intended one), then lexicographic order for determinism.
3. No match → a *missing* link: rendered dashed in notes, drawn as a ghost
   node in the graph, one tap from becoming a real note.

Derived views, all computed from one consistent `Snapshot()`:

- **Links** — a note's outgoing wikilinks, resolved, deduplicated by target.
- **Backlinks** — one entry per referring note with the first mention's line
  as a snippet and the total mention count ("linked mentions").
- **Graph** — nodes for every note plus ghost nodes for missing targets;
  deduplicated directed edges; per-node in/out degree.
- **Search** — case-insensitive substring over names (ranked first) and
  content (with the first matching line as a snippet), capped at 100 results.

**Renames rewrite links.** `Mesh.Rename` moves the file and rewrites every
wikilink that resolved to the old path — preserving aliases and headings,
leaving code fences alone — using the bare name if it is unambiguous after the
move and the full path otherwise. This is the one operation where the server
must be in the loop (a bare `mv` orphans the mesh), and the reason the API has
a rename endpoint instead of treating rename as delete+create.

## 4. The API (`internal/api`)

Small and boring on purpose. Snake_case JSON, integer flags, explicit `""`/
`[]` over nulls where the value is a collection, `{"error": …}` bodies,
statuses 200/201/204/400/404/409 (+502 for cloud). The contract is pinned by
`api_test.go` / `cloud_test.go` and mirrored by the client's
`src/api/client.ts` / `src/api/cloud.ts` — change them only together.

| Endpoint | Purpose |
| --- | --- |
| `GET /api/health` | status, version, note count |
| `GET /api/notes` | list every note (info only) |
| `POST /api/notes` | create (`path`, or `name`+`dir`, name sanitized) |
| `GET /api/notes/{path}` | content + resolved links + backlinks |
| `PUT /api/notes/{path}` | save content (optional `base_mtime_ms`) |
| `DELETE /api/notes/{path}` | delete the file |
| `POST /api/rename` | move + rewrite referring wikilinks |
| `GET /api/search?q=` | name matches first, then content matches |
| `GET /api/graph` | nodes (ghosts included) + deduplicated edges |
| `GET/PATCH /api/cloud/sync` | cloud sync settings (tokens redacted) |
| `POST /api/cloud/sync/connect · /complete · /disconnect` | OAuth lifecycle |
| `GET /api/cloud/sync/callback` | OAuth redirect landing (→ `/sync?cloud=…`) |
| `GET /api/cloud/sync/folders` | folder picker listing |
| `POST /api/cloud/sync/run` | zip + upload right now |
| `GET /api/cloud/sync/snapshots` | `.vault.zip` snapshots in the folder, newest first |
| `POST /api/cloud/sync/restore` | download a snapshot and replace the vault (local backup first) |
| `PUT/DELETE /api/cloud/sync/providers/{id}` | per-deployment OAuth app setup |

Concurrency is optimistic and file-native: a save may carry `base_mtime_ms`,
the mtime the edit started from. If the file moved beneath the editor (another
device, another program), the server answers 409 and the client offers
"load theirs / keep mine". No locks, no CRDTs — honest last-writer-wins with a
warning, which matches the reality of a personal notes app on a LAN.

Domain errors map centrally in `handleErr`: `ValidationError`→400,
`NotFoundError`→404, `ExistsError`→409, `cloud.ConfigError`→400,
`cloud.ProviderError`→502, anything else→500.

## 5. The client (`apps/web`)

A Vite + React PWA with the same chrome conventions as its sibling project
CountRoster: sticky header with the brand/version lockup, desktop nav that
collapses into a bottom tab bar + FAB on phones, the developer mark in the
header corner. Installable (service worker + manifest); the worker never
caches `/api` — notes must be live.

Decisions worth keeping:

- **Markdown renders to React elements, not HTML.** The renderer
  (`src/lib/markdown.tsx`) is hand-rolled: ~250 lines covering the subset that
  matters for notes (headings, emphasis, code, lists incl. tasks, quotes,
  rules, links, images, wikilinks; single newlines become line breaks).
  Because output is built from text, note content cannot inject markup, and
  wikilinks are real router links carrying their resolution state.
  Dependency-free is a feature — don't swap in a markdown-to-HTML library.
- **The editor is a textarea.** Mobile keyboards, autocorrect, undo and IMEs
  all behave; a contenteditable "live preview" is where mobile editors go to
  die. Formatting comes from a small toolbar; typing `[[` pops a suggestion
  chip bar fed by the note list. Autosave debounces at ~1s against the loaded
  mtime; a 409 surfaces the theirs/mine choice.
- **The client holds no business logic.** Link resolution shown in a note
  comes from the server's own `links` array; search, backlinks, rename
  rewriting and the graph are all server-computed.
- **The graph settles up front.** A deterministic Fruchterman–Reingold layout
  run to completion, then rendered as static SVG — calm, cheap on phones, and
  the same vault always draws the same map. Ghost nodes are dashed and create
  the note on tap.
- **Daily notes are a route** (`/today` → `journal/YYYY-MM-DD.md`), so the tab
  bar can carry them and they're linkable.

## 6. Cloud sync (`internal/cloud`)

The vault's off-site copy, ported from CountRoster's automatic cloud backup:
on a user-chosen schedule (hourly / daily / weekly / monthly, or on demand)
the server zips the **whole vault** and uploads
`thoughtmesh-<stamp>.vault.zip` to a folder in the user's Dropbox. Upload is
one-way — the files on the server stay the source of truth, and a snapshot in
Dropbox is an ordinary zip of markdown, readable without Thought Mesh — but
the trip back exists too: **restore** lists the `.vault.zip` snapshots in the
chosen folder and replaces the vault with a selected one.

Restore is engineered to be regret-proof, in this order:

1. Download and structurally validate the archive (`vault.RestoreZip` rejects
   traversal and absolute paths, hidden segments, backslashes, oversized or
   over-populated archives — a hostile zip leaves the vault untouched).
2. Write a **local pre-restore backup** of the vault as it stands, beside the
   settings file (never inside the vault): the undo button.
3. Unpack to a temporary directory first, then swap: clear the vault's
   non-hidden entries and move the staged tree in. Hidden entries (`.git`,
   `.obsidian`) survive, exactly as they're excluded from snapshots.

A restore is a true replace, not a merge — notes created after the snapshot
disappear (they're in the pre-restore backup), which is the only semantics
that doesn't resurrect deleted notes.

Three layers, and only one of them knows a third party exists:

- **`Provider`** — everything account-specific: the OAuth dance, folder
  browsing, uploading bytes. Base URLs are struct fields so tests point them
  at `httptest` servers. Dropbox today; the registry keeps room for more.
- **`Service`** — the provider-agnostic domain: settings, credential
  resolution, PKCE flows, token refresh (with a 2-minute skew), run execution
  and outcome recording.
- **`Scheduler`** — a goroutine polling once a minute. Deliberately a poller,
  not a timer per schedule: `next_run_at` is persisted, so a server that was
  off over its deadline finds the run overdue on the first tick after waking.

Rules that must hold:

- **Settings and tokens live OUTSIDE the vault** in a 0600, atomically
  written JSON file (`cloud.Store`; default `thoughtmesh-cloud.json` beside
  the vault directory, `--cloud-settings` to move it). The vault is exactly
  what users sync by other means — a token inside it would leak with the
  first `git push`. Tokens are likewise redacted from every API response.
- **No shipped OAuth identity.** Providers pre-register redirect URIs and a
  self-hosted origin is unpredictable, so each deployment registers its own
  Dropbox app. The client id is entered on the Sync page (stored in the
  settings file) with `--dropbox-client-id` as the fallback; the settings
  entry wins. Changing the effective client id disconnects the account —
  tokens belong to the client that minted them.
- **Two connect modes.** *Redirect* (provider → `cloud.CallbackPath`, which
  bounces the browser to `/sync?cloud=…`) needs a pre-registered https
  origin; *paste-a-code* asks for no redirect URI at all and is the escape
  hatch for plain-http LAN servers. A code issued without a redirect URI must
  be redeemed without one, so the pending record threads the exact URI (or
  its absence) through to the exchange. PKCE (S256) either way; pending
  authorizations are in-memory, single-use, and expire after 15 minutes.
- **Failures re-schedule, never tight-retry.** The usual causes (revoked
  access, deleted folder) need a human; the outcome is recorded and shown on
  the Sync page.

## 7. Versioning, releases, deployment

- **Calendar versioning `vYEAR.MONTH.<commit count>`** (the scheme from
  sand-vault): `Year`/`Month` are hand-bumped constants in
  `server/internal/version/version.go` — deliberately not the build clock,
  which would move the version without a commit — and the patch number is the
  git commit count. `scripts/version.mjs` is the only assembler; it feeds
  `-ldflags -X` for the binary and Vite `define` for the bundle, so the app
  header and `/api/health` always agree. The month is never zero-padded (that
  keeps tags valid semver); shallow clones are refused (they'd silently
  report patch 1); patch `0` marks an unstamped dev build.
- **Releases** (`.github/workflows/release.yml`): pushing a `v*` tag or a
  `release/v*` branch builds static `linux/{arm64,amd64}` binaries with the
  PWA embedded, publishes them with `.sha256` files, takes the body from the
  matching `## <tag>` section of `CHANGELOG.md`, and refuses a tag that
  doesn't equal the version that commit builds.
- **Deployment** (`scripts/quickstart.sh`): one command installs or upgrades
  a hardened systemd service, from source or from a release asset. Idempotent
  and data-safe: the vault lives outside the source tree, is tar-snapshotted
  before every upgrade while the service is quiesced, and a failed health
  check rolls back to the previous commit or binary. The reference unit is
  `deploy/thoughtmesh.service`.

## 8. Trust model

There is **no auth**, by design, matching CountRoster: the server is meant
for a trusted network — a LAN, a Tailscale tailnet, a VPN. Anyone who can
reach it can read and write every note. What the server still refuses to do:

- hand out OAuth tokens (redacted from the API, 0600 on disk, outside the
  vault and therefore outside every snapshot and copy);
- serve or accept paths outside the vault root;
- render note content as HTML (the client builds React elements from text).

Operators wanting HTTPS or exposure beyond the LAN should front it with
Tailscale Serve or a reverse proxy — the PWA install prefers https anyway,
and the OAuth redirect flow requires it.

## 9. Testing strategy

- Go tests live next to the code, one suite per package, each building a
  fresh vault in `t.TempDir()`. The API tests pin the wire contract the PWA
  compiles against; the cloud tests drive a scriptable fake provider through
  connect → folder → schedule → run, and the Dropbox provider against
  `httptest` servers (paste-mode exchange must send no `redirect_uri`;
  refresh must keep the refresh token; uploads must carry the right args).
- Web tests (vitest, jsdom, globals off) cover the markdown renderer —
  including "raw HTML never renders" and "code fences stay literal" — and
  the time helpers.
- Golden rule inherited from the versioning scheme: never assert the literal
  version string; its *shape* is pinned by `internal/version/version_test.go`.

## 10. Non-goals (for now)

- **Auth/multi-user** — trusted-network deployment (see §8).
- **Attachments/images served from the vault** — the renderer shows remote
  images; serving vault-local files is future work in the server's static
  layer (cloud sync already includes them in snapshots).
- **Real-time collaborative editing** — 409-on-conflict is the intended
  model.
- **Two-way cloud sync** — Dropbox gets snapshots; it never writes into the
  vault. Bidirectional file sync belongs to tools built for it (Syncthing,
  git), which the plain-folder vault already welcomes.
- **Plugins/themes** — the mesh primitives (files, links, graph) come first.
  See `docs/FEATURES.md` for the full product comparison with Obsidian.
