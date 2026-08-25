# Changelog

Release notes, newest first. The release workflow
(`.github/workflows/release.yml`) publishes the `## <tag>` section matching the
pushed tag as that release's body — keep the format:

```
## v2026.8.42 — short title

- bullet points
```

## Unreleased

- Notes can carry **categories**. Give a note any number of them from the note
  screen or the new-note form, and filter the vault by one from the home page
  (the filter lives in the URL, so it's a place you can link to and go back
  to). They are stored in the note's own YAML frontmatter — the same place
  Obsidian looks — so a category travels with the file, shows up in `grep`, and
  survives the note leaving Thought Mesh entirely. There is no registry and no
  "create a category" step: a category exists as long as some note claims it.
  Renaming one rewrites every note that carries it, the way renaming a note
  already rewrites its wikilinks; renaming onto a name already in use merges
  the two. Frontmatter written by other tools is copied through untouched.
- **Dropbox sync is now a real two-way sync.** The chosen folder holds your
  vault as an ordinary directory tree — same notes, same folders, same names —
  instead of a pile of periodic `.vault.zip` snapshots. Notes edited on another
  device come back down, deletions propagate in both directions, and each run
  reports what it moved. The server remembers what both sides held when they
  last agreed (outside the vault, beside the settings file), which is what lets
  it tell "edited here" from "edited there" from "deleted" rather than
  restoring notes you meant to remove.
- **Conflicts are yours to settle.** When the same note changed in both places,
  neither version is touched: the path is parked, the rest of the tree syncs,
  and the Sync page shows both versions with three ways out — keep mine, use
  the cloud's, or merge. The merge is a three-way one against the version they
  diverged from: edits to different parts of a note combine by themselves, and
  only the lines both sides rewrote come back marked for you to settle, in a
  box you can edit before saving. Whichever you pick, both sides end up holding
  it. The same three choices now appear in the editor when a save lands on a
  note that moved underneath it — previously it was load-theirs or keep-mine.
- Before any sync that would replace or delete a note in your vault, the server
  zips the vault as it was. "Undo a sync" on the Sync tab puts one back; the
  five most recent are kept. They live beside the settings file, never inside
  the vault.
- Frontmatter is no longer rendered as note content — a categorised note used
  to open with a stray horizontal rule and a line of YAML above its title.
- The home page opens on a composer: type a note and save it, without a form
  or a name field in the way. The first line becomes the file name (markdown
  stripped, links reduced to the words they show); a name already in the
  vault picks up a " 2", " 3" … suffix rather than failing, and an
  unnameable note falls back to a timestamp. "New note" and the "+" button
  now put the caret in that box; `/new` remains for a note that wants a name
  or folder of its own, linked from under the composer.
- Phone keyboards no longer zoom the page in: the note editor's textarea was
  under 16px, which makes iOS Safari scale the viewport on focus and leave it
  there, so the layout ran off the right edge afterwards. Every control that
  takes a caret is pinned to 16px on narrow screens, as in CountRoster.
- Notched phones: the header no longer sits under the status bar when the
  PWA is installed. The shell asked for `viewport-fit=cover` but never inset
  itself, so the clock and battery painted over the brand lockup. Adopted
  CountRoster's safe-area block — the header pads by
  `env(safe-area-inset-top)`, the shell by the left/right insets, the footer
  by the bottom one, and the editor toolbar's sticky offset follows the
  header's new height.
- Restore from cloud: the Sync tab lists the `.vault.zip` snapshots in the
  connected Dropbox folder and restores the vault from any of them. The
  server validates the archive (zip-slip and size guards), writes a local
  pre-restore backup beside the settings file, and stages to a temp
  directory before swapping — so a failed restore leaves the vault
  untouched and a successful one is undoable. The Dropbox scope now
  includes `files.content.read`; accounts connected before this need a
  reconnect before restoring.
- Automatic cloud sync to Dropbox, ported from CountRoster's cloud backup:
  the server zips the whole vault and uploads it to a chosen Dropbox folder
  on a schedule (hourly/daily/weekly/monthly) or on demand from the new Sync
  tab. OAuth via redirect or paste-a-code (PKCE), per-deployment app
  registration from the UI, tokens kept in a 0600 settings file outside the
  vault, schedule persisted so missed deadlines run on the next tick.
- Calendar versioning (`vYEAR.MONTH.<commit count>`), adopted from sand-vault.
- Initial implementation: Go server over a markdown vault (wikilinks,
  backlinks, rename-with-rewrite, search, graph) + installable PWA client
  (editor with `[[` autocomplete, linked mentions, graph view, daily notes),
  CountRoster-style versioning, quickstart installer and release workflow.
