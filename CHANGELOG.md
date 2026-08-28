# Changelog

Release notes, newest first. The release workflow
(`.github/workflows/release.yml`) publishes the `## <tag>` section matching the
pushed tag as that release's body — keep the format:

```
## v2026.8.42 — short title

- bullet points
```

## Unreleased

- **The graph draws each cluster properly.** A vault is rarely one connected
  web — it's several clusters plus a few notes that link to nothing. One force
  layout over all of that let the clusters repel each other into the corners,
  and fitting the result on screen shrank every one of them to dust. Each
  connected cluster now gets its own layout at full size, tiled side by side.
  A picker above the graph (shown when there's a real choice) focuses one
  cluster — named for its best-connected note — or the unlinked strays, or
  shows everything at once.

- **Folders and categories are now one thing: a note's folder is its
  category.** A note used to be able to sit in `Money/` *and* carry a "Money"
  category, which showed the same word twice on the same card with nothing to
  tell the two apart — and "rename" meant different things depending on which
  one you touched. The folder won, because it was already real: `ls`, `grep`
  and Obsidian all see it, and it travels with the file with no metadata to
  keep in step.

  **Your existing categories are converted on the next start.** Each note
  carrying `categories:` moves into a folder named for its first one and loses
  the frontmatter key. A note with several keeps the first — a file lives in
  one directory — and the server's log names every category it had to drop, and
  every note it left alone because the destination name was taken. History is
  checkpointed first, so the whole rearrangement is one entry you can roll
  back. Any other frontmatter your notes carry is copied through untouched.

  The trade is deliberate: **one category per note**. Many-per-note grouping
  belongs to tags (`#tag`), which are still on the list and are a different
  idea rather than a second spelling of this one.

- **Browse and manage your folders.** A new Folders tab shows the whole tree
  with note counts, including folders that only hold other folders. Rename one
  and its notes move with it — every wikilink pointing into it is rewritten, so
  nothing dangles. Remove one and its notes move up a level; no note is ever
  deleted. A note's own page files it, moves it between folders, or unfiles it.

- **The note header is one button again.** A note used to carry Edit, History,
  Rename and Delete side by side, which on a phone wrapped into a row of
  look-alike buttons with Delete a thumb's width from the one you wanted. Edit
  (or Done) is now the only button; version history, rename and delete moved
  into a "…" menu beside it. The control that files a note moved too — it's a
  chip beside the note's own folder instead of a separate button, and opening
  it puts the picker in a panel of its own.

- **Your vault is now a git repository, and every version of every note is
  kept.** The server commits it a couple of minutes after you stop writing, and
  around every sync — so a note can show you what it said last week and put
  that version back, and the whole vault can be rolled back to any recorded
  point. Nothing is ever rewritten: a rollback is itself recorded as a new
  version, so it can be undone the same way, and `git log` in your vault reads
  the lot without Thought Mesh in the loop. Commit messages carry the time and
  what moved; a manual sync can carry a note of your own ("before the trip"),
  which is the only thing that will tell one of them apart six months later.
  You can also mark a moment deliberately from the Sync tab.

  It needs `git` on the server. Without it the startup log says so and
  everything else works as before — cloud sync keeps writing zip backups
  instead. `--history=off` (or `THOUGHTMESH_HISTORY=off`) turns it off.
  A vault that is already your own git repository is used as it is: its history
  isn't restarted and its `.gitignore` isn't touched.

  The repository stays on the server — `.git` is hidden, so cloud sync already
  skips it. Syncing a `.git` directory through a file-sync service is a known
  way to corrupt one, and with one server owning the vault there is nothing to
  gain by it.
- Where there is a history, cloud sync no longer writes a zip of the vault
  before a run that would replace notes locally: the commit it takes instead is
  the same safety copy and a better one — incremental, diffable, and per-note
  rather than all-or-nothing. The zips remain on servers without git.
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
