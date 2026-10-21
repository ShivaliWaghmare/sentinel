# ADR-0006: App slugs are stable and never reused
Status: Accepted

## Context
An app's slug names its test folder (agents/tests/<slug>/). Deleting an app removes its database rows but leaves its test files on disk, because manual tests may be hand-written work.

## Decision
- The slug is generated from the name when the app is created: lowercase ASCII letters and digits, everything else collapsed to single dashes, at most 50 characters.
- If the slug is taken, a numeric suffix is added (shoplite-2, shoplite-3, …). A slug is taken when an app uses it or when agents/tests/<slug>/ already exists on disk.
- The slug never changes. Renaming an app keeps it, and PATCH rejects a slug field.

## Consequences
- A new app never silently inherits a deleted app's tests, and test paths stored in the database stay valid.
- After a rename, the slug can differ from the name (Renamed → still shoplite).
- Re-adding a deleted app gets a new folder; to reuse its old tests, move the files by hand.
