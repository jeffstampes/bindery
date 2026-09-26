# Calibre/CWA authoritative-library mode — design contract

Status: **accepted, implemented through the backend metadata audit (#6).**
The review UI (#7) and any Calibre write-back (#8) remain planned. The
configuration and implemented slices below are part of this branch.

Tracking: [#1](https://github.com/jeffstampes/bindery/issues/1) ·
upstream [vavallee/bindery#2782](https://github.com/vavallee/bindery/issues/2782)

## Why this exists

Bindery's existing Calibre read path ([library import](../Calibre-Integration-Wiki.md#reading-an-existing-calibre-library))
copies an existing Calibre library into Bindery's own catalogue. Once that has
happened, a book the operator owns has two representations: the Calibre row and
the Bindery row, each maintained by a different tool, each convinced it is
correct.

For an operator whose Calibre or Calibre-Web-Automated library is already the
curated, hand-corrected source of truth — with metadata edited in CWA, and books
arriving from outside Bindery entirely — that second representation is not a
feature. It is the Readarr-and-Calibre ownership model the operator is trying to
leave: the acquisition manager ends up owning the library's metadata.

Authoritative-library mode removes the second authority instead of trying to
keep the two in sync.

## The authority boundary

The split is by *question asked*, not by table:

| Question | Authority |
|---|---|
| Which books exist in the library, and what is their metadata? | **Calibre/CWA** |
| Which authors and series are monitored? | **Bindery** |
| What does the world's catalogue say exists (works, editions, release dates)? | **Bindery** |
| Which works are wanted, and what is their acquisition state? | **Bindery** |
| Is a given external work already owned? | **Derived** — Calibre answers "is it here", Bindery records the match |

Bindery is not becoming stateless. It keeps its database for monitoring, wanted
state, external catalogue metadata, editions, downloads, exclusions and history.
The narrow claim is that **an already-owned library item must not have two
competing metadata authorities**.

## Invariants

These are binding on every slice of this feature. A change that cannot hold them
is out of scope for authoritative-library mode.

1. **Opt-in, default off.** `calibre.authoritative_library_enabled` defaults to
   `false`. A fresh install and an upgraded install behave identically to
   current Bindery until an operator turns it on. There is no migration that
   seeds it, and no condition under which Bindery enables it on the operator's
   behalf.
2. **Existing behaviour is untouched while off.** The write integration
   (`calibre.mode`), the CWA ingest mirror (`cwa.ingest_path`), library import
   (`calibre.library_import_enabled`) and the drop-folder topology all keep
   their current semantics. Turning the mode on must not silently disable any of
   them either; where they genuinely conflict, say so in the UI rather than
   reaching in.
3. **`metadata.db` is read-only.** The core feature opens Calibre's database for
   reading and never writes to it. This is not a convention to be relaxed when
   something would be easier with a write.
4. **Presence in Calibre is not an import.** A Calibre book does not become a
   Bindery catalogue book merely because it exists in the owned library. That is
   precisely what the existing library import does, and it is what this mode
   exists to avoid.
5. **The persisted cross-reference stays minimal.** Bindery may store the link
   between an external work identity and a Calibre book ID, plus the provenance
   and confidence of the match. It may not store a shadow copy of the owned
   book's metadata under the guise of a cross-reference.
6. **Provider metadata is evidence, not authority.** OpenLibrary, Hardcover,
   Google Books and friends describe what the world thinks a book is. In this
   mode they never outrank what Calibre holds for an owned copy.
7. **Discrepancies are advisory and human-reviewed.** An audit may report that
   Calibre's series index disagrees with a provider's. It may not act on that
   disagreement.
8. **Writes back to Calibre are a separate, narrow capability.** If one ever
   ships it is its own opt-in, initially limited to a single Bindery-owned
   mismatch tag, and it never edits an operator's curated fields.
9. **Large libraries are the normal case.** The motivating library is ~80,000
   books. Any design that is only tractable at a few thousand does not satisfy
   this contract.

## Configuration

One key, through Bindery's existing settings machinery — no dedicated handler,
no environment variable, no migration.

| Key | Type | Default | Meaning |
|---|---|---|---|
| `calibre.authoritative_library_enabled` | bool | `false` | Calibre/CWA is authoritative for the metadata of books it already holds |

- Read and written through `GET`/`PUT /api/v1/setting/{key}` like every other
  setting, and described in the settings registry
  (`GET /api/v1/settings/descriptors`), so it is typed, defaulted and
  discoverable without a client hard-coding it.
- Rendered on **Settings → Calibre**, under the read-side section, next to
  Library import. All of its user-facing text flows through `useTranslation`.
- Accepts `true`, `false`, and empty/unset (which reads as off); rejects invalid values such as `yes`.

### Dependency rule

Authoritative mode is designed to read `metadata.db` out of the configured Calibre library, so the two
keys are validated against each other:

- enabling the mode with no `calibre.library_path` is refused;
- clearing `calibre.library_path` while the mode is on is refused.

Turning the mode **off** is always allowed, with or without a library path, so
an instance can never be locked into it.

### Relationship to the existing Calibre settings

| Setting | Interaction |
|---|---|
| `calibre.library_path` | Shared. Authoritative-library mode reads `metadata.db` from it, read-only. Required by the dependency rule above |
| `calibre.library_import_enabled` | Mutually exclusive in execution. When `calibre.authoritative_library_enabled = true`, scheduled and manual Calibre sync operations execute authoritative reconciliation (`Reconcile`) against `metadata.db` read-only and update cross-references, bypassing legacy catalogue import so shadow Book rows are never imported. When authoritative mode is disabled (`false`), legacy library import executes as before |
| `calibre.mode` (write integration) | Unaffected. Registering a *newly acquired* book with Calibre is Bindery handing over a book Calibre does not yet have, which does not cross the authority boundary |
| `cwa.ingest_path` | Unaffected, for the same reason |
| `import.mode = external` | Unaffected. Complementary, if anything: the external tool owning the library is the topology this mode is designed around |

## Implementation slices

Each slice is checked against the authority invariants above.

| Slice | Status | Scope | Key invariants |
|---|---|---|---|
| Live read-only owned library | Implemented (#3) | Read `metadata.db` as a live owned-book source | 3, 9 |
| Work ↔ Calibre matching | Implemented (#4) | Identifier-first matching, conservative author/title fallback, with provenance and confidence | 4, 5, 9 |
| Owned-state integration | Implemented (#5) | A confident match marks a Bindery work owned/satisfied, without importing its metadata | 4, 5 |
| Metadata audit | Implemented (#6) | Compare Calibre's metadata for owned books against stored external evidence and persist advisory findings | 6, 7, 9 |
| Audit/review UI | Planned (#7) | Surface discrepancies for human decision | 7 |
| `BinderyMismatch` write-back | Planned (#8) | Optional, separately opt-in, one Bindery-owned tag | 3, 8 |

Reconciliation satisfies ebook ownership for confident matches and runs the
backend metadata audit. It creates no Calibre-backed catalogue book. There is
no review UI (#7) or Calibre write-back (#8) in this slice.

### Backend metadata audit (#6)

`AuthoritativeService.Reconcile` reuses its single read-only Calibre snapshot
and bulk-loaded Bindery work/edition/identifier data. `AuthoritativeService.Audit`
can independently recheck against a fresh read-only snapshot and fresh stored
external evidence without updating cross-references. Both are gated on the
opt-in setting. Only currently confident, revalidated ebook matches whose
Bindery work has a recognised external identity are comparable; ambiguous,
stale, excluded, Calibre-/ABS-originated and manual-only records are not
independent provider evidence.

The audit compares stored provider work IDs and provider-identified **ebook**
edition ISBNs/ASINs against Calibre's identifiers; unattributed work ASINs,
audiobook editions and imported/local editions cannot serve as ebook provider
evidence. Invalid ISBNs remain reportable identifier values, but never establish
an exact match or select an edition. Titles and languages prefer a uniquely
identifier-matched provider edition when available, otherwise use the stored
external work. All Calibre language values, not just the primary one, participate
in the language comparison.
Authors come from externally identified author rows; series/positions come from
provider-identified `series_books` links. Publication years are compared **only**
for a unique provider edition sharing an identifier with the owned copy (never
against a work's original release date). Multiple provider editions can agree
with a Calibre ISBN without asserting that an unrelated publication date is
wrong. Locked work title/language values are not treated as provider evidence.

Normalization is field-specific and conservative: valid ISBN-10/13 become the
same ISBN-13; ASIN case and known identifier prefixes are canonicalized;
titles/authors/series use the existing Unicode-safe search fold (authors also
support `Last, First`); language uses `NormalizeLanguageCode`; series positions
are numeric to hundredths; publication dates compare years, not months/days.
No fuzzy title or series matching discards substantive words. Disagreements
are advisory (`needs_review` or `ambiguous`), never automatic corrections.

Findings contain the linked Bindery/Calibre IDs, field/type, original values,
stored-source labels and IDs, match method/confidence, a reason and a state.
They live in Bindery's finding table, **not** as a shadow Calibre catalogue.
`unresolved` can be human-marked `ignored` with an optimistic fingerprint
check. A recheck keeps the ignore only when normalized compared values, current
match method/confidence and relevant provider edition/series identity are
unchanged; formatting-only evidence updates retain it. If a match temporarily
disappears, the finding becomes `unmatched` while retaining the ignored
fingerprint, so the *identical* comparison can restore the ignore. Changed
values or match evidence reopen it. Equal current values make a previous
finding `resolved`; a missing confident match or lost comparable evidence makes
it `unmatched`, not clean. No HTTP review endpoint or UI is included until #7.

The standalone audit reads Calibre's headers/authors/formats/identifiers/language
in five bulk SQL queries (plus the read-only open probe), without a per-book
cover-file stat, and Bindery's books, identifiers, editions, cross-references,
previous findings and series links in six bulk queries. Comparisons use indexed
maps; only changed finding rows are written, in a single Bindery transaction
with batches of 50. Deleted books are filtered in the write statement rather
than aborting the pass. Audits and reconciliations sharing one service instance
are serialized so a slower pass cannot replace newer findings. Space and work
are linear in library size, stored evidence and finding count, not per-book
database queries. The existing #5 reconciliation path still calls `GetBook`
while revalidating prior matches; that earlier matching behavior is unchanged
by the audit. The standalone `Audit` method avoids that path entirely.

## Decisions worth restating

**Why `metadata.db` rather than the CWA API.** Bindery already reads Calibre's
schema (`internal/calibre/reader.go`), and a read-only file open has no auth, no
rate limit and no API version to track. A CWA API path makes sense later for
deployments that cannot share the library filesystem; it is not the first
implementation.

**Why a setting rather than inferring the mode.** "The operator has a Calibre
library path configured" is not consent to change who owns their metadata.
Inferring it would violate invariant 1 in the least visible way possible.

**Why not just extend library import with a flag.** Library import's job is to
produce Bindery catalogue rows. Authoritative-library mode's job is to *not*
produce them. Sharing a code path would put the two intents one boolean apart in
a function whose entire purpose is the behaviour being avoided.

## See also

- [`docs/Calibre-Integration-Wiki.md`](../Calibre-Integration-Wiki.md) — the
  existing Calibre and CWA topologies
- [`docs/ARCHITECTURE.md`](../ARCHITECTURE.md) — package layout
- [`internal/calibre/reader.go`](../../internal/calibre/reader.go) — the existing
  `metadata.db` reader
