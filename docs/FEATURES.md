# Thought Mesh vs. Obsidian

An honest, feature-by-feature comparison. Thought Mesh is built as a
self-hosted replacement for how its author uses Obsidian — linked markdown
notes, from every device — not as a clone of Obsidian's whole surface area.
This document says plainly what matches, what's missing, and what the
different architecture buys.

Obsidian is a mature commercial product with a decade of polish and thousands
of community plugins. Where a row below says "no", assume Obsidian does it
well. The comparison is against Obsidian as commonly used (core plugins),
noted where a paid add-on (Sync, Publish) or community plugin is the usual
answer. Accurate as of 2026; Obsidian details may drift.

## The architectural difference (read this first)

Everything else follows from one fork in the road:

- **Obsidian is local-first.** Each device holds its own copy of the vault in
  a native app; multi-device means *file synchronization* — Obsidian Sync
  (paid), iCloud, Syncthing, git — with per-device installs and occasional
  sync conflicts.
- **Thought Mesh is client-server.** One server (your hardware: a Raspberry
  Pi, a home server, a VPS) owns the single copy of the vault; every device
  is a thin, installable web client over the same HTTP API. There is nothing
  to synchronize between devices, because there is only one vault — and
  nothing works without reaching the server.

Consequences, both ways:

| | Obsidian (local-first) | Thought Mesh (client-server) |
| --- | --- | --- |
| Offline use | ✅ fully offline | ❌ needs the server reachable (LAN/VPN/Tailscale) |
| Multi-device consistency | sync lag, conflict files possible | ✅ one copy, always current; 409 + "load theirs / keep mine" on a true race |
| Per-device setup | install app, configure sync per device | open a URL, "Add to Home Screen" |
| Where notes live | every device | one machine you control (put it on a UPS, back it up) |
| Latency | zero (local disk) | LAN round-trip (fast, but real) |
| Works on a borrowed/locked-down machine | ❌ needs the app | ✅ any modern browser |

## Data & portability

| Feature | Obsidian | Thought Mesh |
| --- | --- | --- |
| Notes are plain `.md` files in folders | ✅ | ✅ identical — an Obsidian vault opens as-is (`.obsidian/` is ignored) |
| No proprietary database | ✅ | ✅ no database at all; derived data recomputed from files |
| Vault portable to other tools | ✅ | ✅ same files; grep/git/Syncthing all work |
| YAML front matter / properties | ✅ parsed, editable UI | ⚠️ preserved verbatim but not parsed — shown as text |
| Attachments (images, PDFs) stored in vault | ✅ managed, embedded | ⚠️ files survive and are carried by cloud sync and backups, but aren't served/rendered yet |
| Multiple vaults | ✅ vault switcher | ⚠️ one vault per server process (run two servers for two vaults) |
| File formats beyond markdown | ✅ views images/PDF/canvas | ❌ markdown only |

## Linking & the mesh

| Feature | Obsidian | Thought Mesh |
| --- | --- | --- |
| `[[Wikilinks]]` | ✅ | ✅ same syntax |
| `[[Note\|alias]]` display text | ✅ | ✅ |
| `[[Note#heading]]` links | ✅ scrolls to heading | ⚠️ parsed and preserved; opens the note, no scroll yet |
| `[[Note#^block]]` block references | ✅ | ❌ |
| Backlinks ("linked mentions") | ✅ panel with context | ✅ per-note section with snippet + mention count |
| Unlinked mentions | ✅ | ❌ |
| Links to not-yet-created notes | ✅ styled, click to create | ✅ dashed style, tap to create (prefilled) |
| Rename updates links everywhere | ✅ in-app option | ✅ always, server-side — aliases/headings preserved, code fences untouched, falls back to full path when the new name is ambiguous |
| Link autocomplete when typing `[[` | ✅ | ✅ suggestion chips, keyboard + touch |
| Markdown-style `[text](url)` links | ✅ | ✅ external links; `![img](url)` for remote images |
| Embeds / transclusion `![[Note]]` | ✅ | ❌ renders as a normal link |

## Editing

| Feature | Obsidian | Thought Mesh |
| --- | --- | --- |
| Live preview (WYSIWYG-ish) editing | ✅ | ❌ deliberate: edit (plain textarea) / view modes with one tap between them — predictable on mobile keyboards/IMEs |
| Reading view rendering | ✅ full CommonMark+ | ⚠️ the note-taking subset: headings, bold/italic/strike, inline + fenced code, quotes, nested lists, task checkboxes, rules, links, images |
| Tables | ✅ incl. editor | ❌ rendered as text |
| Callouts, footnotes, math (LaTeX), Mermaid | ✅ | ❌ |
| Formatting toolbar (mobile) | ✅ | ✅ bold/italic/code/heading/list/task/quote/wikilink |
| Quick capture | ⚠️ new-note command, then a name | ✅ the home page opens on a composer — type, Save, keep typing; the first line names the file |
| Autosave | ✅ | ✅ ~1s debounce, with saved/saving status |
| Edit conflict safety | sync-dependent (conflict copies) | ✅ mtime check → 409 → explicit "load theirs / keep mine" |
| Undo history | ✅ editor + file recovery snapshots | ⚠️ browser textarea undo within a session; history = your backups/git |
| Vim mode, multiple panes, tabs | ✅ | ❌ one note at a time |

## Search

| Feature | Obsidian | Thought Mesh |
| --- | --- | --- |
| Full-text search | ✅ | ✅ server-side, name matches ranked first, line snippets |
| Search operators (`path:`, `tag:`, regex) | ✅ | ❌ plain case-insensitive substring |
| Tags (`#tag`) as first-class objects | ✅ | ❌ they're just text (searchable) |
| Categories on a note | ⚠️ via tags or frontmatter properties | ✅ first-class: assign, change and rename them; the notes list filters by one. Stored as `categories:` in the note's own YAML frontmatter, so Obsidian sees them as a property and `grep` finds them |
| Quick switcher | ✅ `Ctrl+O` fuzzy | ⚠️ the search box covers it; no dedicated fuzzy switcher |

## Graph

| Feature | Obsidian | Thought Mesh |
| --- | --- | --- |
| Whole-vault graph | ✅ interactive physics | ✅ force-directed, settled up front (calm + cheap on phones), deterministic layout |
| Ghost nodes for missing notes | ✅ | ✅ dashed; tap to create |
| Local graph (per-note) | ✅ | ❌ |
| Graph filters/groups/colors | ✅ | ❌ node size reflects degree; that's it |

## Daily notes, templates, automation

| Feature | Obsidian | Thought Mesh |
| --- | --- | --- |
| Daily notes | ✅ core plugin | ✅ Today tab → `journal/YYYY-MM-DD.md`, created on first visit |
| Templates | ✅ | ❌ (daily note gets a date heading) |
| Command palette / hotkeys | ✅ | ❌ |
| Plugins (community ecosystem) | ✅ ~2000+ | ❌ none, by design — the mesh primitives come first |
| Themes / CSS snippets | ✅ | ⚠️ follows system light/dark; no theming |
| Canvas / whiteboard | ✅ | ❌ |
| Publish to the web | 💰 Obsidian Publish | ❌ (though the server *is* a web app on your network) |

## Multi-device, sync & backup

| Feature | Obsidian | Thought Mesh |
| --- | --- | --- |
| Same data on every device | 💰 Obsidian Sync, or DIY (iCloud/Syncthing/git) | ✅ inherent — one server, every client sees the same vault instantly |
| Mobile apps | ✅ native iOS/Android | ✅ installable PWA (Add to Home Screen), no app store |
| End-to-end encrypted sync | ✅ with Obsidian Sync | n/a — data never leaves your network unless you enable Dropbox sync |
| Version history | 💰 Sync history; File Recovery core plugin | ✅ built in: the vault is a git repository the server commits a couple of minutes after you stop writing and around every sync. A note shows its own earlier versions and can be put back to one; the whole vault can be rolled back — itself recorded as a new version, so that is undoable too. Needs `git` installed |
| Roll back a mistake | ⚠️ File Recovery snapshots, per file | ✅ per note or the whole vault, from any recorded version, with `git log` in the vault as the escape hatch |
| Cloud sync | 💰 Obsidian Sync, or DIY | ✅ built-in two-way sync with a folder in your Dropbox (hourly/daily/weekly/monthly or on demand). The folder is your vault as an ordinary directory tree, so notes edited or deleted elsewhere come back; OAuth app registered per deployment, tokens stored outside the vault |
| Conflict resolution | ⚠️ Sync keeps both copies as separate files | ✅ neither version is overwritten: keep mine, take theirs, or merge — with a three-way merge that combines edits to different parts of a note and marks only what genuinely collides. Same three choices when a save lands on a note that moved underneath the editor |
| Restore from backup | reinstall + resync | ✅ the server zips the vault before any sync that would replace or delete a note locally; "Undo a sync" on the Sync tab puts one back (and a backup is just a zip, so unzipping by hand works too) |

## Openness, cost, privacy

| | Obsidian | Thought Mesh |
| --- | --- | --- |
| Source | proprietary (free to use) | ✅ open source, AGPL-3.0-only |
| Cost | free; Sync/Publish paid; commercial license for work use (policy has varied) | free, self-hosted; your only cost is the hardware you already run |
| Telemetry | minimal, closed client | none; the server serves you and no one else |
| Data leaves your machines | only via sync/publish | only if you enable Dropbox sync |
| Auth | n/a (local app) | ⚠️ none — trusted-network deployment (LAN/Tailscale/VPN); see ARCHITECTURE.md §9 |

## When to choose which

**Choose Obsidian** if you need offline-first editing on planes and trains,
rich markdown (tables, math, callouts, embeds), the plugin ecosystem, block
references, or polished native apps — and you're happy managing per-device
sync.

**Choose Thought Mesh** if you want the Obsidian *data model* — plain
markdown, wikilinks, backlinks, graph — with zero sync management: one
self-hosted server, every device a browser tab or home-screen app, always
looking at the same notes, with scheduled two-way sync to your own Dropbox and
an AGPL codebase you can read and change. They aren't mutually
exclusive: point Obsidian at a copy of the vault (or the vault itself over a
network share) whenever you want its editor — the files are the same.

## Known gaps most likely to matter (roughly in order)

1. Attachments: serving and rendering vault-local images.
2. `#heading` scroll targets and per-note local graph.
3. Tables and a broader markdown subset in the renderer.
4. Tags as first-class (categories are; `#tag` is still plain text), and
   search operators.
5. A diff view between two versions of a note (today you read one and put it
   back; the comparison is `git diff` in the vault).
6. Unlinked mentions.
7. Templates beyond the daily note.

Gaps are gaps, not promises — but the architecture (server-side mesh, thin
client) has room for all of these without new storage or new dependencies.
