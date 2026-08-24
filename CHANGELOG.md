# Changelog

Release notes, newest first. The release workflow
(`.github/workflows/release.yml`) publishes the `## <tag>` section matching the
pushed tag as that release's body — keep the format:

```
## v2026.8.42 — short title

- bullet points
```

## Unreleased

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
