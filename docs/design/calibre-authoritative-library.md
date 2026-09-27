# Calibre/CWA authoritative-library mode — design contract

Status: **accepted, implemented through read-only owned-edition resolution (#29).**
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
3. **`metadata.db` stays read-only for the core feature.** The authoritative
   reader opens Calibre's database for reading only. The independently opted-in
   #8 mismatch-tag writer and #28 identifier-add writer are separately bounded
   exceptions; neither shares the reader's handle.
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
8. **Writes back to Calibre are separate, narrow capabilities.** The fixed
   `BinderyMismatch` tag is independently opt-in; #28 adds a second independent
   opt-in for human-approved additions of missing, rooted work/provider IDs.
   Neither path replaces arbitrary curated metadata or changes ebook files.
9. **Large libraries are the normal case.** The motivating library is ~80,000
   books. Any design that is only tractable at a few thousand does not satisfy
   this contract.

## Configuration

Three independent opt-ins, through Bindery's existing settings machinery — no
new handler, environment variable, or configuration migration.

| Key | Type | Default | Meaning |
|---|---|---|---|
| `calibre.authoritative_library_enabled` | bool | `false` | Calibre/CWA is authoritative for the metadata of books it already holds |
| `calibre.audit_tag_write_enabled` | bool | `false` | Manage only the `BinderyMismatch` tag for actionable audit findings; requires authoritative mode |
| `calibre.identifier_write_enabled` | bool | `false` | Permit individually approved, evidence-backed additions of missing work/provider identifiers; requires authoritative mode |

- Read and written through `GET`/`PUT /api/v1/setting/{key}` like every other
  setting, and described in the settings registry
  (`GET /api/v1/settings/descriptors`), so it is typed, defaulted and
  discoverable without a client hard-coding it.
- The authoritative opt-in is rendered on **Settings → Calibre** under the
  read-side section next to Library import. The separate audit-tag and
  identifier-write opt-ins are available through the typed settings API.
- All three accept `true`, `false`, and empty/unset (off); invalid values
  such as `yes` are rejected. Enabling either write capability requires
  authoritative mode already enabled. Turning authoritative mode off stops
  both write paths even if their settings remain stored; disabling either
  write setting never reverts edits already made.

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
| `calibre.library_import_enabled` | Mutually exclusive in execution. When `calibre.authoritative_library_enabled = true`, scheduled and manual Calibre sync operations execute authoritative reconciliation (`Reconcile`) using a read-only `metadata.db` snapshot and update cross-references, bypassing legacy catalogue import so shadow Book rows are never imported. The independent audit-tag opt-in may then manage only its tag after the audit commits. Identifier adds require a separate review action and are never part of that pass. When authoritative mode is disabled (`false`), legacy library import executes as before |
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
| Human-approved identifier additions | Implemented (#28; additions only) | Separately opted-in review/approval of uniquely supported missing work/provider IDs; no replacement, removal, edition resolution or bulk | 3, 7, 8 |
| Owned edition resolution | Implemented (#29; read-only) | Resolve only exact canonical-work provider editions, model artifact/write-back lineage, and use selected editions in advisory audit | 3, 6, 7 |

Reconciliation satisfies ebook ownership for confident matches and runs the
backend metadata audit. It creates no Calibre-backed catalogue book. The
review UI never edits curated metadata by itself; the optional #8 tag writer
and individually approved #28 identifier additions are separate exceptions.

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

### Bindery-rooted identity evidence (#21)

Ownership and bibliographic identity are deliberately asymmetric. The existing
confident work ↔ Calibre cross-reference answers **whether the ebook is owned**;
`books.foreign_id` and its configured metadata provider answer **which work**
Bindery means. Every identifier and metadata field on the owned Calibre/CWA
record is a claim to compare *after* resolving the Bindery work. A set of ISBN,
OpenLibrary, Google and Hardcover IDs copied from one erroneous CWA selection
is one correlated source, never several independent votes. No CWA identifier
seeds cross-provider discovery or replaces the canonical foreign ID.

The discovery pass resolves the canonical work through its named provider,
then follows provider-supplied, checksum-valid edition/work ISBNs to the other
configured metadata providers; title/author search is a weaker, bounded
candidate-finding fallback. Raw provider observations stay separate from
interactive search's deduplicated winner, with each lookup's method, seed,
provider, outcome and last-check time. An empty result differs from a failed,
truncated or unconfigured lookup. Candidates remain candidates; neither a
search hit nor a matching CWA claim silently becomes a `book_identifiers` alias.
Confidence groups by independent provider record rather than by the number of
identifiers on that record. Work confidence never implies that the physical
owned file is a particular edition; publication-date comparisons remain
edition-scoped and conservative.

Bindery stores the resulting provider evidence, lookups and *identifier claims*
under the matched work and Calibre ID in dedicated `calibre_identity_*` tables
(migration 092), not in `books`, Calibre's `metadata.db`, or the ownership
cross-reference's match details. This is a refreshable advisory snapshot, not
another catalogue or a proposed correction queue. Reconcile and audit revalidate
stale evidence and recompare current CWA claims; a partial lookup remains visible
for retry rather than becoming negative evidence. Discovery uses at most four
work calls at a time (and four provider calls per work); each pass attempts at
most 256 stale works in a two-minute window. A fifteen-minute background tick
advances the backlog without rerunning the entire audit. Successful snapshots
expire after seven days; a failed or incomplete lookup is retried after six
hours. The admin-only `GET /api/v1/calibre/identity/{bookID}` exposes the
latest work, edition, lookup and claim statuses. The pass is opt-in with
authoritative mode; normal book-add and non-authoritative search paths are
unchanged. No CWA metadata correction, mismatch-tag write-back change, or
file-content inspection is part of identity discovery.

On Book Detail, an ebook already satisfied by Calibre/CWA no longer offers an
ebook indexer search. A dual-format work can still search for its missing
audiobook alone, without searching ebook categories again.

### Owned artifact identifier observations (#25)

Calibre's Extract ISBN plugin reads ebook *content* and may update Calibre's
`metadata.db` identifier, but that database records only the current ISBN, not
whether it came from an owned file. Agreement with that ISBN is therefore **not**
artifact provenance. Calibre's documented `ebook-meta`/`calibredb` interfaces do
not expose the plugin's file-by-file candidates, and the distroless Bindery image
does not include Calibre's GUI/plugin runtime. Instead, an admin explicitly
requests `POST /api/v1/calibre/identity/{bookID}/scan` after a confident ownership
match and rooted provider discovery. It reads the actual linked Calibre files
without modifying them or `metadata.db`. `GET /api/v1/calibre/identity/{bookID}`
then includes `artifacts` alongside the existing provider evidence and CWA
claims. The audit finding comparison and write paths do **not** consume these
observations; later edition-resolution/reconciliation issues may do so.

The initial reader supports **EPUB only**: it reads checksum-valid ISBN-10/13
from package identifier elements and ISBN-labelled XHTML content. It does not
run the Calibre Extract ISBN plugin or retroactively attest to its past actions.
Other formats (PDF, MOBI, KFX, etc.) are explicitly `unsupported`; oversized,
malformed or truncated EPUBs are `partial`/`failed`, not negative evidence.
The scan is on demand per work, never a full-library background rescan. It
allows at most eight formats per request; for EPUBs it limits the file to
64 MiB, 256 archive members, 2 MiB per member, 16 MiB total inspected text
and 32 candidate observations. Unsupported formats are classified before the
EPUB size check and are not hashed or parsed.
No new dependency or network lookup is required. Only paths from the live
Calibre `data` table are opened, confined to the library root (Go `os.Root`).

Migration 093 stores one file-scan record in Bindery per work/Calibre ID,
format and filename, independently of the provider snapshot lifecycle. Each
record retains its library-relative path, format, filename, size, modification
time, scan time, extraction method, completion status and any observed ISBNs
(raw and normalized), their OPF/XHTML source and archive member. SHA-256 and a
digest-derived correlation group are populated only when an EPUB reaches the
hashing step; unsupported formats and EPUBs rejected by the size limit have
neither. Where present, identical digests share a group across formats. The
scanner does not select an ISBN: `selected=false` for every candidate, rather
than claiming Calibre selected the first/last or that a textual mention proves
an edition.
If the file or its Calibre format link changes, the API marks the scan stale and
suppresses its positive assessment; rescanning is explicit. A same-size,
same-mtime replacement cannot be detected cheaply on every GET; an explicit
rescan can detect it (and compare a newly generated digest, where available).

On read, candidates are revalidated against **current** Bindery-rooted provider
work and edition records, never against CWA's ISBN. A matching rooted or
corroborated provider ISBN is `matches_work`; a known rejected provider record
with the same ISBN is `conflict`; an unknown ISBN remains `unverified` (absence
from provider results is not proof of another work). Only a unique edition
from the canonical provider's exact-editions lookup may be `candidate` for
edition confidence; different or conflicting identifiers in one file keep it
`unresolved`. Neither label selects the physical edition or alters work
confidence, `books.foreign_id`, `book_identifiers`, cross-references, CWA
metadata or ebook bytes. An old Calibre link's scans are not exposed as current
when ownership changes.

### Read-only owned-edition resolution (#29)

`GET /api/v1/calibre/identity/{bookID}` now exposes a separate `edition`
resolution (`exact`, `high`, `ambiguous`, `unresolved`), a reason and all eligible
candidate editions. Only ebook editions from the canonical provider's *exact
editions of the established work* are candidates. Work confidence is unchanged;
wrong-work artifact IDs and cross-provider search results cannot redirect the
root or create ownership. `exact` requires a unique valid ISBN in a complete,
current file scan with an explicit original-file attestation; `high` is a unique
CWA/native-edition-ID or ISBN claim, a unique shared-ISBN tie-break from complete
edition language/year, or limited historical-file support. All CWA fields are
one correlated source; all ISBNs in one file are one source. Provider fields
from one record are not independent votes. Conflicts, multiple competing ISBNs,
missing/partial/stale file evidence and ambiguous or truncated provider edition
lists do not force a winner. A lone unknown-provenance artifact scan is never
independent. The read-only Calibre snapshot exposes publisher/imprint, language
and publication year for conservative shared-identifier tie-breaks (only when
all competing provider editions supply a comparable value); original *work*
publication date is never used.

`POST /api/v1/calibre/identity/{bookID}/scan` accepts optional
`{"attestOriginal":true}` when an admin can explicitly attest that the linked
original ebook predates all known Calibre-to-file write-backs. Default scans
carry **unknown** independence. Known earlier reports reject an original-file
attestation. Admins can report an *external* Polish Books / Calibre-to-file
operation using `POST /api/v1/calibre/identity/{bookID}/writeback` with
`{"writtenAt":"<RFC3339 timestamp>","source":"polish_books"}` (or
`"calibre_to_file"`). Reporting is a Bindery-only provenance record, not an
operation on CWA or the ebook. Migration 096 retains superseded scans with
original timestamps, digests and attestation in `artifactHistory`, and exposes
reported events under `writebacks`. An observation before a known write-back is
labelled `pre_writeback`, but is not presumed independent without explicit
attestation; one after is `potentially_cwa_derived`. Without a report or
attestation, lineage stays `unknown`. A report after a current scan makes that
scan stale even if the filesystem timestamp did not change. A post-write-back
scan cannot independently corroborate a CWA claim or attain `exact`. A past
attested observation that agrees with a *current scan of the same linked file*
may provide limited `high` support, never exact physical-file certainty.
Unreported external write-backs cannot be detected automatically.

Audits bulk-load the current scans, historical observations and reported events
from Bindery; current file stats guard against stale evidence. The selected
canonical provider edition, when exact/high, supplies edition title, language
and publication year; when ambiguous/unresolved, publication year is not
asserted from a work release date. Edition-scoped identifier comparisons prefer
the selected edition instead of aggregating every edition of the work. This
changes no CWA metadata, ebook bytes, #28 writer eligibility or #30 field
reconciliation.

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
check. Subsequent rechecks refresh the evidence underneath an ignored finding
without silently reopening it, even when material values or match method change
for the same owned Calibre book. If a match temporarily disappears, the finding
becomes historical `unmatched` while retaining the prior ignore; a returning
comparison for the same owned book restores it. A match to a different Calibre
book does not inherit that ignore. A reviewer may explicitly Reopen the currently
ignored comparison, clearing the active ignore but retaining the earlier human
decision in append-only history. Equal current values still make a previous
finding `resolved` automatically; a missing confident match or lost comparable
evidence makes it `unmatched`, not clean. The fingerprint guards the exact
comparison visible when a reviewer acts; it no longer acts as an automatic
reopen trigger. #7 supplies the review API/UI and #26 extends its lifecycle.

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

### Review queue (#7, #26)

Admins see **Activity → Calibre audit** (and a Settings → Calibre link) only
when authoritative mode and a library path are configured. With the mode off,
review endpoints return 404 and the ordinary app/navigation stays unchanged.
The default queue shows unresolved findings; reviewers may filter by state,
finding type, assessment and identifier scope (work/provider vs ISBN/ASIN/
OpenLibrary edition). This scope is a **review-only taxonomy** for identifier
findings, not an edition-identity or confidence calculation: `asin` findings
come from provider-identified ebook editions, while a Calibre ASIN alone may
refer to another format and does not establish the owned edition. The
`edition` filter selects edition-oriented discrepancies for human review; it
never promotes a claim to an exact edition match. Quick queues separate
current needs-review comparisons, ambiguous edition identifiers, ignored
findings and historical unmatched rows.
Historical rows display last recorded values, not confirmed current mismatches;
current comparisons remain advisory, not automatically actionable corrections.
Compact source labels distinguish Calibre, provider work/edition, and discovered
provider evidence. Valid provider-native IDs link to canonical records only
where a safe URL is known (OpenLibrary work/edition/author, Google volume,
Hardcover nonnumeric slug, DNB numeric record). Unsupported IDs stay plain text.
Ownership-match confidence is labelled separately from edition confidence;
identity context is fetched on demand from the existing admin endpoint rather
than querying per finding on a large review page. Candidate editions are never
presented as confirmed owned editions. Review actions are per finding; there is
no bulk mutation of ambiguous evidence.

Admin-only API routes under `/api/v1/calibre/audit`:

- `GET ?state=&findingType=&assessment=&identifierScope=&limit=&offset=`
  returns `{items,total,limit,offset}` with newest-first stable ordering;
  default 50, maximum 250 rows. SQL counts filtered matches and decodes only
  one page, using a single join for the work title; one additional bounded
  query loads review decisions for that page only. No Calibre metadata is copied
  into the Bindery catalogue.
- `POST /{id}/ignore` takes `{comparisonFingerprint}` and returns 204 only for
  the displayed, still-unresolved comparison; a stale decision returns 409.
- `POST /{id}/reopen` takes the current `{comparisonFingerprint}` and returns
  204 only for an ignored, still-current comparison; stale or non-ignored rows
  return 409. Both actions are admin-only, atomic with an append-only event in
  Bindery (migration 094). Existing ignored rows get one migration-time event;
  older decisions that are no longer represented cannot be reconstructed.
  Reopening clears the active ignore, never edits Calibre metadata, and runs the
  optional mismatch-tag projection after commit, just like Ignore.
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

### Explicit operator reconciliation (#27)

In authoritative-library mode, **Activity → Calibre audit → Run reconciliation**
starts `POST /api/v1/calibre/reconciliation` (admin-only); poll
`GET /api/v1/calibre/reconciliation/status`. This is separate from the
existing audit-only Recheck. The accepted `202` run is tracked until process
shutdown and does not inherit the request's cancellation; a duplicate run or
simultaneous manual recheck gets `409`. Scheduled/startup/import passes continue
to use the same service mutex, so the operator run cannot overlap their
snapshot/write work, but they are not shown as manual runs. The state is
process-local (restart resets to idle): `idle`, `running`, `completed`, `partial`
(provider/incomplete discovery), or `failed`. A running status names the stage
and completed stage boundaries, not a guessed percentage; errors retain any
ownership counts already computed.

The existing `Reconcile` machinery first matches/revalidates ownership from a
single read-only Calibre snapshot, then refreshes **at most 256** stale
Bindery-rooted identity snapshots within its two-minute provider window, then
compares/persists advisory audit findings. The manual response reports new
matches, revalidations, stale and unmatched works; newly collected work/evidence
counts; a single indexed count of stored file scans for current ownership links;
current failed, truncated, unconfigured and unattempted provider
lookups; deferred stale works and unresolved canonical roots (or unavailable
discovery); audit compared/current/updated counts; and
persisted finding transitions (newly unresolved, resolved, ignored decisions
preserved, newly historical). A failed/unconfigured lookup is **not** a negative
match. The report is the last explicit run only, not a historical run database.
A second unchanged run reuses fresh provider snapshots and does not rewrite
unchanged ownership links or audit findings; ignored decisions remain intact.

The explicit run does **not** invoke the optional `BinderyMismatch` tag writer,
even if its independent opt-in is enabled. Existing scheduled/audit tag behavior
is unchanged. It does **not** rescan any ebook files: artifact evidence is
separately collected on demand per book through `POST /identity/{bookID}/scan`,
and a full ~19,000-book rescan would be costly and would change the meaning of
this bounded operation. Existing artifact rows remain available on per-book
identity reads; the run summary reports only newly collected provider evidence.
No Calibre/CWA curated field or ebook file is modified.

### Optional audit-tag write-back (#8)

`calibre.audit_tag_write_enabled` defaults to `false` independently of
`calibre.authoritative_library_enabled`. Enable it with an admin `PUT
/api/v1/setting/calibre.audit_tag_write_enabled` and body `{"value":"true"}`
only after configuring authoritative mode and a writable Calibre library.
There is no tag-name or field-name setting. This tag writer touches only
`BinderyMismatch`; the separately opted-in identifier-add writer below is a
second, disjoint exception. Ownership, matching and review still work when
this setting is off. Disabling it stops all tag
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

### Human-approved CWA identifier additions (#28)

This is a **different, default-off opt-in** (`calibre.identifier_write_enabled`)
from authoritative reads and the mismatch-tag writer. An admin must explicitly
preview a current missing-identifier finding at `GET
/api/v1/calibre/audit/{id}/identifier-proposals` and approve one returned value
via `POST /api/v1/calibre/audit/{id}/identifier-add` with its
`comparisonFingerprint` and `proposedValue`. `GET
/api/v1/calibre/audit/{id}/identifier-attempts` lists recent attempts for
inspection/retry decisions. The UI shows missing → proposed, rooted evidence,
provider record links where a safe direct URL exists, and a distinct approval
control; following a link is navigation only. A stale fingerprint/value returns
409; no browser-supplied field, Calibre ID, evidence ID or SQL is authoritative.

Eligibility is deliberately narrower than advisory audit comparison: the
finding must be current, unresolved, `needs_review`, `identifier_missing`, and
work-level (Open Library work, Google, Hardcover or DNB only); its only proposed
value must be confirmed by a fresh exact canonical provider record or high
confidence ISBN lookup rooted in that record. The Bindery work and its exact or
high, currently matched Calibre ownership link must still agree with the
read-only live Calibre snapshot and persisted rooted evidence. CWA claims do
not seed or corroborate provider identity. Edition IDs, ISBNs, ASINs, artifact
ISBNs and other file observations cannot authorize a write: Polish Books may
have copied CWA values into an ebook and these observations do not yet prove
independence. Existing identifiers cannot be replaced or removed; #29 must
resolve the owned edition before any higher-risk path. No bulk or unattended
write is implemented: an aggregate finding count alone does not prove every
row independently meets the rooted threshold.

**Mutation boundary.** `internal/db/calibre_identifier_writer.go` has one
public mutation, `AddMissing(ctx, Calibre book ID, fixed-allowlisted provider
type, validated value)`, bound to a library root at construction. It opens a *separate* `metadata.db` handle in
`mode=rw` (no database creation), checks Calibre's `application_id` (`cali`),
starts a `BEGIN IMMEDIATE` transaction, verifies the book exists and that no
record for its identifier type or another book's same type/value exists, then
executes a parameterized, conditional **`INSERT INTO identifiers (book, type,
val)`**. The service reruns the live ownership match, book fingerprint and
rooted-evidence check while the SQLite write reservation is held, so another
Calibre edit cannot invalidate approval between validation and insert.
Canonical type aliases and supported stored prefixes are considered when
checking type occupancy and duplicate work claims. It commits only when exactly
one row is inserted; any error or failed
precondition rolls back. It does not expose its connection or generic SQL, and
contains no UPDATE or DELETE of Calibre rows. No `books`, `tags`,
`books_tags_link`, formats, authors, language, cover, ebook file or other table
is a writer target; tests exercise preservation of unrelated rows and values.
External Calibre writers may contend on the SQLite lock; `busy_timeout` is
bounded to five seconds and errors leave the finding/evidence intact. Calibre
apps caching metadata may need a refresh to display the added ID.

Migration 095 stores attempted adds in Bindery with actor user ID (0 when no
user ID is available), finding/ownership IDs, field, old/new value, evidence
keys, action, time, outcome and error. An intent is stored as `pending` before
opening a writable Calibre handle; rejected or failed attempts are retained.
Only a committed Calibre write is marked `applied`; an interrupted attempt
remains pending, not falsely successful, and must be inspected before retrying.
After commit an ordinary audit re-reads Calibre and resolves the finding only
if its current comparison agrees. If the re-audit fails, the committed write
remains recorded and the operator can use the existing recheck route. Human
ignore/reopen history and identity evidence are never rewritten by a failed
identifier transaction.

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
