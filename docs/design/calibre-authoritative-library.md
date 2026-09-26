# Calibre/CWA authoritative-library mode — design contract

Status: **accepted, implemented through optional mismatch-tag write-back (#8).**
The configuration and implemented slices below are part of this branch.

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
3. **`metadata.db` is read-only for the core feature.** The authoritative
   reader opens Calibre's database for reading only. The separately opted-in
   #8 mismatch-tag writer is the sole sanctioned exception; it has no access
   to the reader's handle and cannot change curated metadata fields.
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
8. **Writes back to Calibre are a separate, narrow capability.** Management
   of the single Bindery-owned `BinderyMismatch` tag is independently opt-in and
   is the **sole sanctioned Calibre metadata write-back exception**. It never
   edits an operator's curated fields or replaces unrelated tags.
9. **Large libraries are the normal case.** The motivating library is ~80,000
   books. Any design that is only tractable at a few thousand does not satisfy
   this contract.

## Configuration

Two independent opt-ins, through Bindery's existing settings machinery — no
new handler, environment variable, or migration.

| Key | Type | Default | Meaning |
|---|---|---|---|
| `calibre.authoritative_library_enabled` | bool | `false` | Calibre/CWA is authoritative for the metadata of books it already holds |
| `calibre.audit_tag_write_enabled` | bool | `false` | Manage only the `BinderyMismatch` tag for actionable audit findings; requires authoritative mode |

- Read and written through `GET`/`PUT /api/v1/setting/{key}` like every other
  setting, and described in the settings registry
  (`GET /api/v1/settings/descriptors`), so it is typed, defaulted and
  discoverable without a client hard-coding it.
- The authoritative opt-in is rendered on **Settings → Calibre** under the
  read-side section next to Library import. The separate audit-tag opt-in is
  available through the typed settings API (no new UI control).
- Both accept `true`, `false`, and empty/unset (off); invalid values such as
  `yes` are rejected. Enabling tag writes requires authoritative mode already
  enabled. Turning authoritative mode off always stops tag writes even if the
  tag-write setting remains stored; turning tag writes off never removes tags.

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
| `calibre.library_import_enabled` | Mutually exclusive in execution. When `calibre.authoritative_library_enabled = true`, scheduled and manual Calibre sync operations execute authoritative reconciliation (`Reconcile`) using a read-only `metadata.db` snapshot and update cross-references, bypassing legacy catalogue import so shadow Book rows are never imported. The independent audit-tag opt-in may then manage only its tag after the audit commits. When authoritative mode is disabled (`false`), legacy library import executes as before |
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
| Audit/review UI | Implemented (#7) | Admin review queue with bounded, filtered findings and ignore/recheck actions | 7 |
| `BinderyMismatch` write-back | Implemented (#8) | Optional, separately opt-in, one fixed Bindery-owned tag | 3, 8 |

Reconciliation satisfies ebook ownership for confident matches and runs the
backend metadata audit. It creates no Calibre-backed catalogue book. The
review UI never edits curated metadata; optional #8 tag management is the
sole sanctioned write-back exception.

### Author detail, Search wanted, and book detail (#18, #20)

Author-scoped book-list responses expose aggregate `effectiveStatus` when all
requested formats are satisfied and, for non-skipped dual-format works,
`effectiveEbookStatus` / `effectiveAudiobookStatus` when authoritative mode is
on; stored `books.status` stays unchanged. The author page uses the aggregate
status for its In library/Wanted counts, badges, and Search wanted count, and
the per-format status for filtered views. A Calibre match satisfies an ebook,
not an audiobook: a dual-format work may be imported in the ebook view and
wanted in the audiobook and aggregate views until audio is also present.
Legacy untyped `filePath` still satisfies only a single-format work.
The author bulk-search endpoint independently applies the same batched
`FilterWantedBooks` ownership check before dispatch, even if a client posts a
search without using the UI. With the opt-in off, no effective statuses are
projected and searches keep their previous behavior.

`GET /api/v1/book/{id}` projects those **same** response-only fields for a
single work, using an indexed cross-reference lookup instead of the author
list's bulk match map. Book detail uses the effective aggregate for its status
badge, and distinguishes an ebook satisfied in Calibre/CWA from an actual
Bindery file in the File section. An ebook-only match with no Bindery file
reads as owned in Calibre/CWA with no local path, download, or delete action;
a dual-format work also shows the audiobook's independent missing/local-file
state. File paths, `bookFiles`, and persisted `status` are never synthesized or
updated by this projection. When the opt-in is off the detail response and
presentation retain their existing stored-status behavior.

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
it `unmatched`, not clean. No HTTP review endpoint or UI was included in #6;
#7 supplies them without changing the audit's lifecycle rules.

The standalone audit reads Calibre's headers/authors/formats/identifiers/language
in five bulk SQL queries (plus the read-only open probe), without a per-book
cover-file stat, and Bindery's books, identifiers, editions, cross-references,
previous findings and series links in six bulk queries. Comparisons use indexed
maps; only changed finding rows are written, in a single Bindery transaction
with batches of 50. Deleted books are filtered in the write statement rather
than aborting the pass. Audits and reconciliations sharing one service instance
are serialized so a slower pass cannot replace newer findings. Space and work
are linear in library size, stored evidence and finding count, not per-book
database queries. Reconciliation (#16) bulk-loads a single coherent read-only
Calibre snapshot (`AllBooks`) and revalidates both new matches and existing
cross-references using the in-memory `LibraryIndex` snapshot without any
per-reference Calibre reads (`GetBook` / `FindByIdentifier`). The standalone `Audit`
method shares that same single-snapshot model.

### Review queue (#7)

Admins see **Activity → Calibre audit** (and a Settings → Calibre link) only
when authoritative mode and a library path are configured. With the mode off,
review endpoints return 404 and the ordinary app/navigation stays unchanged.
The default queue shows unresolved findings; reviewers may filter by state,
finding type and assessment, including historical `unmatched`/`resolved` rows.
Each row shows Calibre's authoritative evidence beside stored external provider
evidence, original source/provenance, match method/confidence, and audit reason.
An ambiguous comparison is explicitly labelled; unmatched rows are historical,
not proof of a current mismatch.

Admin-only API routes under `/api/v1/calibre/audit`:

- `GET ?state=&findingType=&assessment=&limit=&offset=` returns
  `{items,total,limit,offset}` with newest-first stable ordering; default 50,
  maximum 250 rows. The SQL counts filtered matches and decodes only one page,
  using a single join to display the Bindery work title. No Calibre metadata is
  copied into the Bindery catalogue.
- `POST /{id}/ignore` takes `{comparisonFingerprint}` and returns 204 only for
  the displayed, still-unresolved comparison; a stale decision returns 409.
  The existing #6 fingerprint lifecycle continues to govern later rechecks.
- `POST /recheck` accepts a full audit of a read-only snapshot as a
  shutdown-tracked background task (`202 {running,startedAt}`); duplicate manual
  starts return `409` with the running snapshot. `GET /recheck/status` returns
  the current or most recent manual run (`running`, timestamps, and either
  `result` or `error`). The UI polls and refreshes the queue on completion;
  leaving the page or disconnecting does not stop an accepted audit. The state
  lives in process memory (a restart clears it), and shutdown cancels/drains
  the tracked job before DB close. The shared service lock still serializes
  manual and scheduled passes; optional tag projection runs only after audit
  persistence, never while a Bindery DB transaction is open.

An optional `cwa.web_url` setting on Settings → Calibre is the browser-facing
CWA base URL. When configured, the UI links a Calibre book ID to CWA's
`/book/{id}` detail route (respecting a configured path prefix). It does not
infer a web URL from the ingest folder or Calibre plugin URL; unsafe or unset
URLs hide the link. This is navigation only, not the #8 write-back feature.

### Optional audit-tag write-back (#8)

`calibre.audit_tag_write_enabled` defaults to `false` independently of
`calibre.authoritative_library_enabled`. Enable it with an admin `PUT
/api/v1/setting/calibre.audit_tag_write_enabled` and body `{"value":"true"}`
only after configuring authoritative mode and a writable Calibre library.
There is no tag-name or field-name setting. Management of `BinderyMismatch`
is the **sole sanctioned Calibre metadata write-back exception**; ownership,
matching and review still work when it is off. Disabling it stops all tag
writes, including removals; it does not clean up existing tags.

The desired tag set comes from committed finding rows: one or more
`unresolved` + `needs_review` findings for a Calibre book means tag present;
ignored, resolved, unmatched and ambiguous findings do not keep it. After each
full audit/reconciliation and each successful review ignore, Bindery makes one
indexed Bindery query for distinct actionable Calibre IDs and one bulk Calibre
query for existing mismatch-tag links, then applies only the differences.
Multiple findings on a book cause at most one change. An ignored last finding
removes the tag immediately; resolution removes it on the next audit. This
tag is a review signal, **not** a statement that Calibre's metadata is wrong:
provider evidence is advisory. When a match is lost, the historical finding
is `unmatched`, not clean, but it cannot authorize a current mismatch tag.

The purpose-built writer opens a separate `metadata.db` handle with `mode=rw`
(never creates a database). It inserts only the fixed tag into `tags`, adds
or removes only its rows in `books_tags_link`, and does not update `books`,
author, series, identifier, cover, comments or other metadata tables. Unlike
`calibredb set_metadata --field tags:...`, it never replaces the whole tag
collection, so unrelated tag links are not replaced. It does not require
`calibredb` in the distroless image; the Calibre library mount must be
writable by Bindery's UID. Calibre applications that cache metadata may need
a refresh to show external SQLite changes, and external concurrent writers
should be tested against the actual library deployment before enabling this
opt-in. Direct SQLite schema changes in future Calibre versions may require
updating this narrowly scoped writer; failure leaves the audit usable.

Audit persistence finishes before any Calibre write. Link differences are
applied in 256-book SQL batches (one transaction per add batch and one atomic
DELETE per removal batch), rather than per-book writes. A tag failure is logged
and reported in the audit result's optional `tagError` (review-ignore keeps its
204 result because the decision was already saved). Earlier batches stay
applied after a partial failure; every later audit retries the entire
projection even if zero findings changed. No Bindery transaction remains open
during external writes. The shared service mutex serializes audits,
reconciliation and review projection within one Bindery process; independent
Bindery instances should not both manage the same library's tag.

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
