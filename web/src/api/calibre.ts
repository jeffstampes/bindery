import { apiURL, request } from './core'

// CalibreMode selects which integration flow runs after a successful
// Bindery import. 'off' skips Calibre entirely, 'calibredb' shells out to
// the calibredb CLI, 'plugin' posts to the Bindery Bridge Calibre plugin.
export type CalibreMode = 'off' | 'calibredb' | 'plugin'

// CalibreSettings mirrors the `calibre.*` keys stored in the settings table.
export interface CalibreSettings {
  calibre_mode: CalibreMode
  calibre_library_path: string
  calibre_binary_path: string
}

export interface CalibreTestResult {
  ok: string
  version: string
  message: string
}

// CalibreImportStats summarises one completed library import. Present
// only on the final poll (when progress.running flips false).
export interface CalibreImportStats {
  authorsAdded: number
  authorsLinked: number
  booksAdded: number
  booksUpdated: number
  editionsAdded: number
  duplicatesMerged: number
  skipped: number
}

// CalibreImportProgress is the polled shape for /calibre/import/status.
// The UI renders a progress bar from total/processed, swaps in the stats
// summary once running=false, and surfaces any error inline.
export interface CalibreImportProgress {
  running: boolean
  startedAt?: string
  finishedAt?: string
  total: number
  processed: number
  message?: string
  error?: string
  stats?: CalibreImportStats
}

// CalibreSyncError is one failed push entry returned by /calibre/sync/status.
export interface CalibreSyncError {
  bookId: number
  title: string
  path?: string
  reason: string
}

// CalibreSyncStats summarises one bulk-push run. Pushed = newly added;
// alreadyInCalibre = 409 Conflict (treated as success for idempotency);
// failed = everything else.
export interface CalibreSyncStats {
  total: number
  processed: number
  pushed: number
  alreadyInCalibre: number
  failed: number
  // skipped counts books the run never attempted. Deliberately not part of
  // total, which is the denominator of the progress bar.
  skipped: number
}

// CalibreSyncSkip is one book the bulk push did not attempt, and why.
// Before these existed a skipped book showed up nowhere at all, so an empty
// report could mean either "already in Calibre" or "every book was dropped by
// a filter you cannot see" (discussion #1592).
export interface CalibreSyncSkip {
  bookId: number
  title: string
  reason: string
}

// CalibreSyncProgress is the polled shape for /calibre/sync/status.
export interface CalibreSyncProgress {
  running: boolean
  startedAt?: string
  finishedAt?: string
  message?: string
  error?: string
  stats: CalibreSyncStats
  errors: CalibreSyncError[]
  // skips samples the skipped books, capped the same way errors is.
  // stats.skipped always holds the full count.
  skips: CalibreSyncSkip[]
}

// CalibreImportRun is one persisted Calibre import run (issue #643). Used
// by the "Recent imports" list in the Calibre settings tab.
export interface CalibreImportRun {
  id: number
  sourceId: string
  libraryPath: string
  status: string
  dryRun: boolean
  sourceConfigJson?: string
  summaryJson?: string
  startedAt: string
  finishedAt?: string
}

export interface CalibreRollbackStats {
  actionsPlanned: number
  entitiesDeleted: number
  provenanceUnlinked: number
  filesAffected: number
  skipped: number
  failed: number
}

export interface CalibreRollbackAction {
  entityType: string
  externalId: string
  localId: number
  displayName?: string
  outcome: string
  action: string
  reason?: string
}

export interface CalibreRollbackResult {
  runId: number
  preview: boolean
  applied: boolean
  dryRun: boolean
  status: string
  stats: CalibreRollbackStats
  actions: CalibreRollbackAction[]
  filesOnDiskWarning?: string
  finishedAt: string
}

export interface CalibreAuditEvidence {
  value: string
  source: string
  provider?: string
  foreignId?: string
  recordId?: number
}

export interface CalibreIdentitySnapshot {
  bookId: number
  calibreId: number
  rootKey: string
  hasOwnedCover?: boolean
  evidence: Array<{
    status: string
    workConfidence: string
    editionConfidence: string
    editionId?: string
    canonicalIdentity?: string
    provider?: string
    method?: string
    provenanceGroup?: string
    key?: string
    checkedAt?: string
    providerMetadata?: { title?: string; publisher?: string; publicationDate?: string; ebook?: boolean; imageUrl?: string }
  }>
  lookups?: Array<{ method: string; outcome: string; provider: string }>
  claims?: Array<{ identifierType: string; status: string }>
  artifacts?: Array<{
    lineage: string
    attestedOriginal: boolean
    stale: boolean
    historical: boolean
    outcome: string
    identifiers: Array<{ status: string; normalizedValue: string }>
  }>
  edition?: {
    confidence: 'exact' | 'high' | 'ambiguous' | 'unresolved'
    editionId?: string
    provider?: string
    reason: string
    // Server-selected presentation category; missing on older snapshots.
    reasonCode?: string
    artifactWarning?: string
    candidates: Array<{ editionId: string; provider: string; reasons: string[]; claims?: Array<{ type: string; value: string }> }>
  }
}

export interface CalibreAuditDecision {
  action: 'ignore' | 'reopen'
  comparisonFingerprint: string
  createdAt: string
}

export interface CalibreAuditFinding {
  id: number
  bookId: number
  bookTitle?: string
  calibreId: number
  field: string
  evidenceKey: string
  findingType: string
  assessment: 'needs_review' | 'ambiguous'
  calibreEvidence: CalibreAuditEvidence[]
  binderyEvidence: CalibreAuditEvidence[]
  matchMethod: string
  matchConfidence: string
  reason: string
  comparisonFingerprint: string
  state: 'unresolved' | 'ignored' | 'resolved' | 'unmatched'
  decisions?: CalibreAuditDecision[]
  createdAt: string
  updatedAt: string
}

export interface CalibreAuditPage {
  items: CalibreAuditFinding[]
  total: number
  limit: number
  offset: number
}

// Server-authored, advisory work-level identifier additions. The write endpoint
// accepts only the value and the comparison fingerprint, never a client-chosen type.
export interface CalibreIdentifierProposal {
  findingId: number
  bookId: number
  calibreId: number
  comparisonFingerprint: string
  identifierType: string
  currentValue: string
  proposedValue: string
  action: string
  evidenceKeys: string[]
  evidence: CalibreAuditEvidence[]
  reason: string
}

export interface CalibreIdentifierAddResult {
  attemptId: number
  outcome: string
  reauditError?: string
}

export interface CalibreIdentifierAttempt {
  id: number
  actorUserId: number
  identifierType: string
  oldValue: string
  proposedValue: string
  evidenceKeys: string[]
  action: 'add' | 'replace' | 'remove'
  outcome: 'pending' | 'applied' | 'failed' | 'rejected'
  error?: string
  createdAt: string
}

export interface CalibreMetadataRefreshEligibility {
  status: 'eligible' | 'ineligible' | 'stale'
  reason?: string
  matchMethod?: string
  confidence?: string
}

export interface CalibreMetadataRefreshField {
  name: string
  current: string
  fetched: string
  status: 'change' | 'unchanged' | 'missing' | 'withheld'
  reason?: string
}

export interface CalibreMetadataRefreshProposal {
  id: number
  bookId: number
  calibreId: number
  rootKey: string
  evidenceKey: string
  edition: CalibreIdentitySnapshot['edition']
  lookupIsbn: string
  currentIdentifiers: Record<string, string>
  proposedIdentifiers: Record<string, string>
  fields: CalibreMetadataRefreshField[]
  fetchedOpfDigest?: string
  sourceDigest: string
  lookupLog?: string
  status: 'ready' | 'no_result' | 'lookup_failed' | 'ineligible'
  reason?: string
  version: number
  fingerprint: string
}

export interface CalibreMetadataRefreshAttempt {
  id: number
  proposalId: number
  actorUserId: number
  bookId: number
  calibreId: number
  action: string
  outcome: 'pending' | 'applied' | 'rejected' | 'partial' | 'failed' | 'verification_failed'
  error?: string
  verification?: string
  startedAt: string
  finishedAt?: string
}

export interface CalibreAuditResult {
  totalCalibreBooks: number
  comparedBooks: number
  findings: number
  updated: number
}

export interface CalibreAuditRecheckStatus {
  running: boolean
  startedAt?: string
  finishedAt?: string
  result?: CalibreAuditResult
  error?: string
}

export interface CalibreReconciliationResult {
  totalCalibreBooks: number
  totalBinderyBooks: number
  matched: number
  revalidated: number
  stale: number
  unmatched: number
  artifactScansCached: number
  identity?: {
    refreshedWorks: number
    evidenceRecords: number
    failedLookups: number
    truncatedLookups: number
    unconfiguredLookups: number
    notAttemptedLookups: number
    deferredWorks: number
    unresolvedRoots: number
    discoveryUnavailable: boolean
  }
  audit?: CalibreAuditResult
  transitions?: {
    newUnresolved: number
    resolved: number
    ignoredPreserved: number
    becameHistorical: number
  }
}

export interface CalibreReconciliationStatus {
  state: 'idle' | 'running' | 'completed' | 'partial' | 'failed'
  stage?: 'ownership' | 'identity' | 'audit'
  completedStages: string[]
  startedAt?: string
  finishedAt?: string
  result?: CalibreReconciliationResult
  error?: string
}

export const calibreApi = {
  // Calibre
  testCalibre: () => request<CalibreTestResult>('/calibre/test', { method: 'POST' }),
  calibreImportStart: () => request<CalibreImportProgress>('/calibre/import', { method: 'POST' }),
  calibreImportStatus: () => request<CalibreImportProgress>('/calibre/import/status'),
  calibreSyncStart: () => request<CalibreSyncProgress>('/calibre/sync', { method: 'POST' }),
  calibreSyncStatus: () => request<CalibreSyncProgress>('/calibre/sync/status'),
  calibreRuns: (limit = 10) => request<CalibreImportRun[]>(`/calibre/runs?limit=${limit}`),
  calibreRunRollbackPreview: (runId: number) =>
    request<CalibreRollbackResult>(`/calibre/runs/${runId}/rollback/preview`),
  calibreRunRollback: (runId: number) =>
    request<CalibreRollbackResult>(`/calibre/runs/${runId}/rollback`, { method: 'POST' }),
  calibreAudit: (params: { state?: string; findingType?: string; assessment?: string; identifierScope?: string; limit: number; offset: number }) =>
    request<CalibreAuditPage>(`/calibre/audit?${new URLSearchParams(Object.entries(params).filter(([, value]) => value !== undefined).map(([key, value]) => [key, String(value)])).toString()}`),
  calibreAuditIgnore: (id: number, comparisonFingerprint: string) =>
    request<void>(`/calibre/audit/${id}/ignore`, { method: 'POST', body: JSON.stringify({ comparisonFingerprint }) }),
  calibreAuditReopen: (id: number, comparisonFingerprint: string) =>
    request<void>(`/calibre/audit/${id}/reopen`, { method: 'POST', body: JSON.stringify({ comparisonFingerprint }) }),
  calibreAuditIdentifierProposals: (id: number) =>
    request<{ items: CalibreIdentifierProposal[] }>(`/calibre/audit/${id}/identifier-proposals`),
  calibreAuditIdentifierAdd: (id: number, body: { comparisonFingerprint: string; proposedValue: string }) =>
    request<CalibreIdentifierAddResult>(`/calibre/audit/${id}/identifier-add`, { method: 'POST', body: JSON.stringify(body) }),
  calibreAuditIdentifierAttempts: (id: number) =>
    request<{ items: CalibreIdentifierAttempt[] }>(`/calibre/audit/${id}/identifier-attempts`),
  calibreMetadataRefreshEligibility: (bookId: number) =>
    request<CalibreMetadataRefreshEligibility>(`/calibre/metadata-refresh/${bookId}/eligibility`),
  calibreMetadataRefreshPreview: (bookId: number) =>
    request<CalibreMetadataRefreshProposal>(`/calibre/metadata-refresh/${bookId}/preview`, { method: 'POST' }),
  calibreMetadataRefreshApply: (id: number, fingerprint: string) =>
    request<CalibreMetadataRefreshAttempt>(`/calibre/metadata-refresh/proposals/${id}/apply`, { method: 'POST', body: JSON.stringify({ fingerprint }) }),
  calibreMetadataRefreshAttempts: (bookId: number) =>
    request<{ items: CalibreMetadataRefreshAttempt[] }>(`/calibre/metadata-refresh/${bookId}/attempts`),
  calibreAuditIdentity: (bookId: number) => request<CalibreIdentitySnapshot>(`/calibre/identity/${bookId}`),
  calibreOwnedCoverURL: (bookId: number) => apiURL(`/calibre/identity/${bookId}/cover`),
  calibreAuditRecheck: () => request<CalibreAuditRecheckStatus>('/calibre/audit/recheck', { method: 'POST' }),
  calibreAuditRecheckStatus: () => request<CalibreAuditRecheckStatus>('/calibre/audit/recheck/status'),
  calibreReconcile: () => request<CalibreReconciliationStatus>('/calibre/reconciliation', { method: 'POST' }),
  calibreReconcileStatus: () => request<CalibreReconciliationStatus>('/calibre/reconciliation/status'),
}
