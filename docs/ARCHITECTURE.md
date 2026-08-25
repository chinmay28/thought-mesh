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
  structure preserved) — now used for the local pre-sync backups.
- **Categories are frontmatter** (`frontmatter.go`). A note's categories live
  in a YAML block at the top of the file itself — the same place Obsidian and
  every other markdown tool looks — so a category travels with the note and
  survives leaving this server. There is no registry, no sidecar, and no
  "create a category" step: a category exists exactly as long as some note
  declares it. The parser is a deliberately tiny one rather than a YAML
  dependency; it reads and rewrites one key (`categories:`, in flow or block
  form; `category:` accepted on read) and copies every other frontmatter line
  through byte for byte, so metadata written by other tools survives intact.
- **File-level access** (`files.go`) sits beside the note API for the callers
  that care about the folder rather than the notes in it — cloud sync mirrors
  the whole tree, attachments included. `CleanFilePath` is looser than
  `CleanPath` (an attachment's name is not ours to choose) and just as strict
  about escaping the root or touching hidden state.

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
- **Categories** — each note's own list, cached on the same `(mtime, size)`
  key as its links, plus the vault-wide vocabulary with note counts. Derived
  like everything else here: the files are the only source of truth.

**Renaming a category rewrites every note that carries it**, for the same
reason renaming a note rewrites its wikilinks — a name means the same thing
everywhere, and leaving half the notes on the old spelling would silently split
one category into two. Matching is case-insensitive, and renaming onto a name
already in use merges the two.

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
| `GET /api/notes` | list every note (info only); `?category=` narrows it |
| `POST /api/notes` | create (`path`, or `name`+`dir`, name sanitized; optional `categories`) |
| `GET /api/notes/{path}` | content + resolved links + backlinks |
| `PUT /api/notes/{path}` | save content (optional `base_mtime_ms`) |
| `DELETE /api/notes/{path}` | delete the file |
| `POST /api/rename` | move + rewrite referring wikilinks |
| `GET /api/search?q=` | name matches first, then content matches |
| `GET /api/graph` | nodes (ghosts included) + deduplicated edges |
| `GET /api/categories` | the vault's vocabulary, with note counts |
| `POST /api/categories/assign` | replace one note's categories (optional `base_mtime_ms`) |
| `POST /api/categories/rename` | rename a category vault-wide (onto an existing one merges) |
| `POST /api/categories/delete` | strip a category from every note carrying it |
| `POST /api/merge` | three-way merge of `base`/`mine`/`theirs` → merged text + conflict count |
| `GET/PATCH /api/cloud/sync` | cloud sync settings (tokens redacted) |
| `POST /api/cloud/sync/connect · /complete · /disconnect` | OAuth lifecycle |
| `GET /api/cloud/sync/callback` | OAuth redirect landing (→ `/sync?cloud=…`) |
| `GET /api/cloud/sync/folders` | folder picker listing |
| `POST /api/cloud/sync/run` | sync now: push, pull, propagate deletions |
| `GET /api/cloud/sync/conflicts` | paths both sides changed, awaiting a decision |
| `GET /api/cloud/sync/conflicts/{path}` | both versions, the base, and a merge of them |
| `POST /api/cloud/sync/resolve` | settle one conflict (`keep_local` / `keep_remote` / `merge`) |
| `GET /api/cloud/sync/backups` | local pre-sync copies of the vault, newest first |
| `POST /api/cloud/sync/backups/restore` | undo a sync from one of those backups |
| `PUT/DELETE /api/cloud/sync/providers/{id}` | per-deployment OAuth app setup |

Concurrency is optimistic and file-native: a save may carry `base_mtime_ms`,
the mtime the edit started from. If the file moved beneath the editor (another
device, another program), the server answers 409 and the client offers three
ways out — **load theirs**, **keep mine**, or **merge**. The merge posts the
three texts to `/api/merge` (only the browser still holds the version the edit
started from) and puts the result back in the editor, marked where the two
sides genuinely collided. No locks, no CRDTs — honest optimistic concurrency
with a real third option, which matches the reality of a personal notes app on
a LAN.

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

A **two-way sync** between the vault and a folder in the user's Dropbox. The
folder holds the vault as a plain directory tree — same notes, same folders,
same names, ordinary markdown readable without Thought Mesh — not an archive of
it. On a user-chosen schedule (hourly / daily / weekly / monthly, or on demand)
each run pushes local changes up, pulls remote ones down, propagates deletions
in both directions, and surfaces anything that changed in both places as a
conflict for the user to settle.

### Why a run needs three versions, not two

Comparing the vault against the folder cannot decide anything on its own. "This
note differs from the one in Dropbox" looks identical whether it was edited
here, edited there, or edited in both places — and a file present on one side
only is equally an addition there or a deletion here. What tells them apart is a
third description: what both sides held when they last agreed.

That record is `internal/cloud/state.go` — a content hash (Dropbox's own
algorithm, so a listing answers "did this change?" with no download) and the
provider's revision handle, per path. Against it, each run derives:

| local vs base | remote vs base | outcome |
| --- | --- | --- |
| unchanged | changed | pull (or delete locally) |
| changed | unchanged | push (or delete remotely) |
| — | — (identical to each other) | nothing; record that they agree |
| changed | changed, differently | **conflict** — touch neither side |

The state lives beside the settings file, **outside the vault**, for the same
reason the tokens do: the vault is exactly what users copy and version by other
means, and bookkeeping that rode along would be restored onto machines it does
not describe. Losing it is survivable — the next run simply has no base, so
anything that differs comes back as a conflict for a human.

### Conflicts are parked, never guessed

A contested path is left exactly as it is on both sides and skipped by every
subsequent run until it is resolved; the rest of the tree syncs normally, so one
argued-over note never blocks the other nine hundred. The user then picks:

- **keep mine** — this server's version is pushed over the remote one (or the
  deletion is re-applied, when this is the side that deleted it);
- **use theirs** — the cloud's version replaces the local one (or the local
  file goes);
- **merge** — `internal/merge` runs a line-level diff3 against the cached base
  version, combining edits to different regions silently and marking only what
  both sides rewrote. The result lands in an editable box, not on disk: a merge
  that guesses is worse than one that asks.

Every resolution ends with **both** sides holding the same bytes and that
agreement recorded — including the merge — because fixing one side alone would
bring the conflict straight back on the next run. The same merge engine backs
`POST /api/merge`, so an editor save conflict and a sync conflict resolve
identically.

The last race a two-way sync has to survive is a write landing in Dropbox
between this run's listing and its upload. Uploads are therefore conditional on
the revision we last saw (`update` mode); a refusal is turned into an ordinary
conflict rather than a failed run.

### Safety

Cloud sync is the one thing here that changes notes the user did not just edit,
so a run that is about to overwrite or delete anything locally zips the vault
first, beside the settings file (never inside it — a backup swept into the next
sync would upload the vault into itself). Those backups are listable and
restorable from the Sync page: the undo button for "the cloud sent down
something I didn't want". Restoring one clears the sync state, since the vault
now holds something the cloud has never seen.

A file-level failure is recorded and the run continues — a note that couldn't
upload is no reason to leave the rest out of step.

Three layers, and only one of them knows a third party exists:

- **`Provider`** — everything account-specific: the OAuth dance, folder
  browsing, listing the remote tree, and per-file upload/download/delete. Each
  provider converts to and from vault-relative paths itself, so the sync engine
  never has to know what a Dropbox path looks like. Base URLs are struct fields
  so tests point them at `httptest` servers. Dropbox today; the registry keeps
  room for more.
- **`Service`** — the provider-agnostic domain: settings, sync state,
  credential resolution, PKCE flows, token refresh (with a 2-minute skew), the
  comparison and its application, conflict resolution, and outcome recording.
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
- **Changing the destination folder resets the sync state**, and so does
  disconnecting. The same path in a different folder is a different file;
  treating the old hashes as a base there would read as "every note was deleted
  remotely" and push that deletion.

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
  vault and therefore outside everything sync, copy or `git push` carries);
- serve or accept paths outside the vault root;
- render note content as HTML (the client builds React elements from text).

Operators wanting HTTPS or exposure beyond the LAN should front it with
Tailscale Serve or a reverse proxy — the PWA install prefers https anyway,
and the OAuth redirect flow requires it.

## 9. Testing strategy

- Go tests live next to the code, one suite per package, each building a
  fresh vault in `t.TempDir()`. The API tests pin the wire contract the PWA
  compiles against; the cloud tests drive a scriptable fake provider — holding
  the remote folder in memory, with the same revision semantics — against a
  *real* vault on disk, through connect → folder → schedule → sync, and the
  Dropbox provider against `httptest` servers (paste-mode exchange must send no
  `redirect_uri`; refresh must keep the refresh token; a recursive listing must
  follow its cursor; a stale-revision upload must report a conflict, not a
  failure). The comparison table itself is tested directly, case by case, with
  no I/O at all.
- Web tests (vitest, jsdom, globals off) cover the markdown renderer —
  including "raw HTML never renders", "code fences stay literal" and
  "frontmatter is not content" — the category picker, and the time helpers.
- Golden rule inherited from the versioning scheme: never assert the literal
  version string; its *shape* is pinned by `internal/version/version_test.go`.

## 10. Non-goals (for now)

- **Auth/multi-user** — trusted-network deployment (see §8).
- **Attachments/images served from the vault** — the renderer shows remote
  images; serving vault-local files is future work in the server's static
  layer (cloud sync already carries them).
- **Real-time collaborative editing** — 409-on-conflict, with a merge offered,
  is the intended model.
- **Sub-file sync granularity** — a sync run compares whole files. Two people
  typing into one note at the same time is what §10's first bullet rules out,
  not what this is for.
- **Plugins/themes** — the mesh primitives (files, links, graph) come first.
  See `docs/FEATURES.md` for the full product comparison with Obsidian.
