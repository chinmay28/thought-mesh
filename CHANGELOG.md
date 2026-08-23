# Changelog

Release notes, newest first. The release workflow
(`.github/workflows/release.yml`) publishes the `## <tag>` section matching the
pushed tag as that release's body — keep the format:

```
## v2026.8.42 — short title

- bullet points
```

## Unreleased

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
