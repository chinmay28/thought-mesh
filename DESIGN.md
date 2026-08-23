# Thought Mesh — Design

This document explains the shape of the system and the reasoning behind the
decisions that are meant to last. `CLAUDE.md` is the working summary; this is
the why.

## 1. What it is

An interconnected note-taking app — the Obsidian model (a *vault* of markdown
files joined by `[[wikilinks]]`) rebuilt as a **client-server** app so every
device shares one vault. A small Go server owns a folder of `.md` files and
serves both a REST API and an installable PWA from one origin.

```
┌─────────────┐   HTTP/REST    ┌──────────────┐    os.ReadFile/WriteFile
│ browser PWA │ ─────────────> │  Go server   │ ─────────────────────────> vault/*.md
│  (apps/web) │ <───────────── │  (server/)   │
└─────────────┘                └──────────────┘
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

Writes are write-to-temp-then-rename so a crash can't truncate a note. Note
paths are validated hard at the vault boundary: no escaping the root, no
dot-segments (which also keeps `.git`/`.obsidian`/`.trash` invisible), and none
of `# | [ ] \ : * ? " < >` — the first four break wikilink syntax, the rest
break Windows and common sync targets.

## 3. Links and the mesh

Wikilink syntax is Obsidian's: `[[Target]]`, `[[Target|shown text]]`,
`[[Target#heading]]`. Parsing skips fenced code blocks. Resolution:

1. A target containing `/` is tried as a vault path (case-insensitive), then
   falls back to its final segment as a name.
2. A bare name matches any note with that file name, case-insensitively.
   Shortest path wins a tie (the "closest to the root" note is almost always
   the intended one), then lexicographic order for determinism.
3. No match → a *missing* link: rendered dashed in notes, drawn as a ghost
   node in the graph, one tap from becoming a real note.

**Renames rewrite links.** `Mesh.Rename` moves the file and rewrites every
wikilink that resolved to the old path — preserving aliases and headings,
leaving code fences alone — using the bare name if it is unambiguous after the
move and the full path otherwise. This is the one operation where the server
must be in the loop (a bare `mv` orphans the mesh), and the reason the API has
a rename endpoint instead of treating rename as delete+create.

## 4. The API

Small and boring on purpose. Snake_case JSON, `{"error": …}` bodies,
200/201/204/400/404/409. The contract is pinned by
`server/internal/api/api_test.go` and mirrored by `apps/web/src/api/client.ts`.

| Endpoint | Purpose |
| --- | --- |
| `GET /api/health` | status, version, note count |
| `GET /api/notes` | list every note (info only) |
| `POST /api/notes` | create (`path`, or `name`+`dir`) |
| `GET /api/notes/{path}` | content + resolved links + backlinks |
| `PUT /api/notes/{path}` | save content (optional `base_mtime_ms`) |
| `DELETE /api/notes/{path}` | delete the file |
| `POST /api/rename` | move + rewrite referring wikilinks |
| `GET /api/search?q=` | name matches first, then content matches with snippets |
| `GET /api/graph` | nodes (ghosts included) + deduplicated edges |

Concurrency is optimistic and file-native: a save may carry `base_mtime_ms`,
the mtime the edit started from. If the file moved beneath the editor (another
device, another program), the server answers 409 and the client offers
"load theirs / keep mine". No locks, no CRDTs — honest last-writer-wins with a
warning, which matches the reality of a personal notes app on a LAN.

## 5. The client

A Vite + React PWA with the same chrome conventions as CountRoster: sticky
header with the brand/version lockup, desktop nav that collapses into a bottom
tab bar + FAB on phones, the developer mark in the header corner.

Decisions worth keeping:

- **Markdown renders to React elements, not HTML.** The renderer
  (`src/lib/markdown.tsx`) is hand-rolled: ~250 lines covering the subset that
  matters for notes. Because output is built from text, note content cannot
  inject markup, and wikilinks are real router links carrying their resolution
  state. Dependency-free is a feature — don't swap in a markdown-to-HTML
  library.
- **The editor is a textarea.** Mobile keyboards, autocorrect, undo and IMEs
  all behave; a contenteditable "live preview" is where mobile editors go to
  die. Formatting comes from a small toolbar; `[[` pops a suggestion chip bar
  fed by the note list. Autosave debounces at ~1s against the loaded mtime.
- **The graph settles up front.** A deterministic Fruchterman–Reingold layout
  run to completion, then rendered as static SVG — calm, cheap on phones, and
  the same vault always draws the same map.
- **Daily notes are a route** (`/today` → `journal/YYYY-MM-DD.md`), so the tab
  bar can carry them and they're linkable.

## 6. Versioning, releases, deployment

All inherited from CountRoster deliberately, so the two projects operate
identically:

- `vMAJOR.MINOR.<commit count>` assembled only by `scripts/version.mjs`;
  stamped into the Go binary by `-ldflags` and into the bundle by Vite
  `define`. Shallow clones are refused (they'd silently report patch 1).
- `.github/workflows/release.yml` publishes static per-arch binaries with the
  PWA embedded and `.sha256` files, body from `CHANGELOG.md`, and refuses a
  tag that doesn't match the commit count.
- `scripts/quickstart.sh` installs/upgrades a hardened systemd service from
  source or from a release asset — idempotent, vault snapshotted (tar.gz)
  before every upgrade, health-checked, self-rolling-back. The vault lives
  outside the source tree so no code operation can touch it.

## 7. Non-goals (for now)

- **Auth/multi-user** — trusted-network deployment, same stance as CountRoster.
- **Attachments/images in the vault** — the renderer shows remote images;
  serving vault-local attachments is future work in the server's static layer.
- **Real-time collaborative editing** — 409-on-conflict is the intended model.
- **Plugins/themes** — the mesh primitives (files, links, graph) come first.
