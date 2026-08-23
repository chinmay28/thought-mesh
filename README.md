# Thought Mesh

Interconnected note taking — a self-hosted replacement for
[Obsidian](https://obsidian.md)-style linked notes, built as a **client-server**
app so every device shares one vault:

- **Your notes are plain markdown files.** The vault is an ordinary folder of
  `.md` files on your server — grep it, `git` it, sync it, open it in any other
  editor (Obsidian included). There is no database; the files *are* the data.
- **Wikilinks and backlinks.** Link notes with `[[double brackets]]`
  (`[[Note]]`, `[[Note|shown text]]`, `[[Note#heading]]`). Every note shows its
  *linked mentions* — who links here, with context. Renaming a note rewrites
  the links that point at it, everywhere.
- **A graph of your mesh.** Every note a dot, every link a thread — including
  “ghost” notes you've linked to but not written yet; tap one to create it.
- **Mobile-friendly PWA.** The web client is installable and behaves like an
  app on a phone — no app store, no native build. Editing, autocomplete for
  `[[links]]`, search, and the graph all work one-handed.
- **Daily notes.** The Today tab opens (or creates) `journal/YYYY-MM-DD.md`.
- **One shared source of truth.** A small Go backend owns the vault; desktop
  and mobile clients all read and write the same notes, with conflict
  detection when two devices edit the same note.
- **No accounts, no auth.** Meant to run on a trusted network (your LAN, a
  Tailscale tailnet, a VPN). Anyone who can reach the server can use it.

## Layout

```
thought-mesh/
├── DESIGN.md                 # architecture & design document
├── server/                   # the Go backend — REST API over the vault, compiles to ONE static binary
│   ├── cmd/thoughtmesh/      #   entrypoint; embeds the built PWA at release time
│   └── internal/             #   vault (file store), mesh (link index), HTTP layer
└── apps/
    └── web/                  # @thoughtmesh/web — installable PWA client (Vite + React)
```

The deployable artifact is a **single static Go binary** (`server/bin/thoughtmesh`)
that serves the REST API and the PWA from one origin, with zero runtime
dependencies — Node is only needed at build time to compile the web client.

## Getting started

```bash
npm install                                   # Node >= 20.10 (build/dev tooling)

# Terminal 1 — the backend API (Go >= 1.23; newer toolchains fetch automatically):
cd server && go run ./cmd/thoughtmesh          # http://localhost:8788

# Terminal 2 — the web client with hot reload:
npm run dev --workspace @thoughtmesh/web       # http://localhost:5173, proxies /api
```

Notes land in `./data/vault` by default; point the server at an existing
folder of markdown (an Obsidian vault works) with
`go run ./cmd/thoughtmesh serve --vault ~/notes`.

### Install on a server (Raspberry Pi, home server, VPS)

One command installs Thought Mesh as a hardened systemd service — idempotent,
re-run it to upgrade in place with automatic vault backup and rollback:

```bash
curl -fsSL https://raw.githubusercontent.com/chinmay28/thought-mesh/main/scripts/quickstart.sh | sudo bash
```

Prefer the prebuilt static binary from a GitHub release (no toolchain at all):

```bash
curl -fsSL https://raw.githubusercontent.com/chinmay28/thought-mesh/main/scripts/quickstart.sh | sudo THOUGHTMESH_INSTALL=release bash
```

See the header of [`scripts/quickstart.sh`](./scripts/quickstart.sh) for every
knob (port, vault location, service user, pinning a branch or release).

## Commands

```bash
npm test             # vitest (web) AND `go test ./...` (server)
npm run build        # web bundle + `go build` → server/bin/thoughtmesh
npm run typecheck    # tsc --noEmit + `go vet`
```

## Versioning

`vYEAR.MONTH.<commit count>` — a calendar version where every commit is a
patch release: `v2026.8.311` is the 311th commit on the 2026.8 line. The year
and month are source constants in
[`server/internal/version/version.go`](./server/internal/version/version.go),
bumped by hand when a release line opens (never taken from the build clock);
the patch number is the git commit count, stamped into the binary and the web
bundle at build time by [`scripts/version.mjs`](./scripts/version.mjs). The
version in the app header and in `/api/health` always agree; a version ending
in `.0` is an unstamped development build.

## License

Thought Mesh is free software under the **GNU AGPL-3.0-only** — see
[`LICENSE`](./LICENSE). It's a network app, so AGPL §13 applies to operators:
if you run a modified Thought Mesh for others over a network, you must offer
them its source.
