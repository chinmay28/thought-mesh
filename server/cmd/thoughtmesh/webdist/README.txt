This directory is the embed target for the built web client.

Release builds copy apps/web/dist here before `go build`, so the binary
carries the whole PWA (see .github/workflows/release.yml and
scripts/quickstart.sh). In a bare checkout it holds only this file and the
server falls back to serving --web-dist / WEB_DIST from disk, or runs
API-only for development against the Vite dev server.

Everything here except this README is gitignored.
