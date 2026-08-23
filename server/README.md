# Thought Mesh server

The Go backend: a REST API over a **vault** — an ordinary folder of markdown
files — plus the built PWA, served from one origin as a single static binary.

```bash
go run ./cmd/thoughtmesh                 # serve on :8788, vault at ./data/vault
go run ./cmd/thoughtmesh serve --vault ~/notes --port 9000
go test ./...
```

Packages:

- `internal/vault` — the note store. Files are the data: path validation,
  list/read/write/create/delete/rename, atomic writes, hidden-entry skipping.
- `internal/mesh` — everything derived: wikilink parsing, resolution,
  backlinks, graph, search, and rename-with-link-rewrite. In-memory only,
  rebuilt from the files on demand (cached per file by mtime+size).
- `internal/api` — the HTTP layer; wire contract pinned by `api_test.go`.
- `internal/version` — calendar version `vYEAR.MONTH.<commit count>`; patch
  stamped at link time (see `scripts/version.mjs` at the repo root).
- `cmd/thoughtmesh` — CLI (`serve`, `version`, `help`), embeds `webdist/`.

Build the deployable binary from the repo root with `npm run build` (compiles
the PWA, then the server with the version stamped in). `CGO_ENABLED=0` always.
