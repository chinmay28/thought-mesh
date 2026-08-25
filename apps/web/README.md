# @thoughtmesh/web

The Thought Mesh web client: a mobile-friendly, installable PWA (Vite + React)
over the server's REST API.

```bash
npm run dev        # http://localhost:5173, proxies /api → http://localhost:8881
npm test           # vitest (jsdom, globals off)
npm run build      # tsc --noEmit && vite build → dist/
```

Notable corners:

- `src/api/client.ts` — the typed HTTP client; shapes mirror the server's wire
  contract exactly (pinned by `server/internal/api/api_test.go`).
- `src/lib/markdown.tsx` — hand-rolled markdown → React renderer (no HTML
  injection possible; wikilinks are router links). Keep it dependency-free.
- `src/components/Editor.tsx` — textarea editor with formatting toolbar and
  `[[` autocomplete chips.
- `src/components/Menu.tsx` — the "…" overflow menu. Secondary actions go here
  rather than into a header row, so each screen keeps one obvious primary
  button.
- `src/components/QuickCapture.tsx` + `src/lib/noteName.ts` — the composer the
  home page opens on. No name field: the first line of the body becomes the
  file stem, and a taken name walks " 2", " 3" … rather than stopping to ask.
  `/new` is still there for a note that wants a name or a folder of its own.
- `src/pages/GraphPage.tsx` — deterministic force layout rendered as SVG.
- The service worker never caches `/api` (see `vite.config.ts`).
