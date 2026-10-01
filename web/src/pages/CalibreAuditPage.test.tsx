import { beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor, fireEvent, within } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import { api, ApiError, type CalibreAuditFinding, type CalibreIdentitySnapshot, type CalibreIdentifierProposal } from '../api/client'
import CalibreAuditPage from './CalibreAuditPage'
import en from '../i18n/locales/en.json'

vi.mock('../api/client', async importOriginal => {
  const original = await importOriginal<typeof import('../api/client')>()
  return { ...original, api: { ...original.api,
    calibreAudit: vi.fn(), calibreAuditIgnore: vi.fn(), calibreAuditReopen: vi.fn(), calibreAuditIdentity: vi.fn(), calibreAuditRecheck: vi.fn(), calibreAuditRecheckStatus: vi.fn(),
    calibreReconcile: vi.fn(), calibreReconcileStatus: vi.fn(),
    calibreAuditIdentifierProposals: vi.fn(), calibreAuditIdentifierAdd: vi.fn(), calibreAuditIdentifierAttempts: vi.fn(), getSetting: vi.fn(),
  } }
})
vi.mock('react-i18next', () => {
  const t = (key: string, opts?: Record<string, unknown>) => {
    const value = key.split('.').reduce<unknown>((node, part) => (node as Record<string, unknown>)?.[part], en)
    return typeof value === 'string' ? value.replace(/{{(\w+)}}/g, (_, part: string) => String(opts?.[part] ?? '')) : key
  }
  return { useTranslation: () => ({ t }) }
})

const finding: CalibreAuditFinding = {
  id: 3, bookId: 5, bookTitle: 'Provider title', calibreId: 17, field: 'title', evidenceKey: '',
  findingType: 'title_difference', assessment: 'ambiguous',
  calibreEvidence: [{ value: 'Owned title', source: 'calibre.books.title' }],
  binderyEvidence: [{ value: 'Provider title', source: 'books.title', provider: 'openlibrary', foreignId: 'OL1W' }],
  matchMethod: 'isbn', matchConfidence: 'exact', reason: 'The owned title differs from the stored external work or uniquely matched edition title; translations and subtitles need human review.',
  comparisonFingerprint: 'fp1', state: 'unresolved', createdAt: '2026-01-01', updatedAt: '2026-01-01',
}

const missing: CalibreAuditFinding = { ...finding, field: 'identifiers', evidenceKey: 'openlibrary', findingType: 'identifier_missing', assessment: 'needs_review', calibreEvidence: [] }
const proposal: CalibreIdentifierProposal = {
  findingId: 3, bookId: 5, calibreId: 17, comparisonFingerprint: 'fp1', identifierType: 'openlibrary',
  currentValue: '', proposedValue: 'OL12W', action: 'add', evidenceKeys: ['openlibrary:OL12W'],
  evidence: [{ value: 'OL12W', source: 'books.foreign_id', provider: 'openlibrary', foreignId: 'OL12W' }],
  reason: 'Exact canonical work or rooted ISBN corroboration; no edition identity asserted.',
}

function renderPage() { return render(<MemoryRouter><CalibreAuditPage /></MemoryRouter>) }

beforeEach(() => {
  vi.clearAllMocks()
  localStorage.clear()
  vi.mocked(api.getSetting).mockResolvedValue({ key: 'cwa.web_url', value: '' })
  vi.mocked(api.calibreAudit).mockResolvedValue({ items: [finding], total: 1, limit: 50, offset: 0 })
  vi.mocked(api.calibreAuditIgnore).mockResolvedValue(undefined)
  vi.mocked(api.calibreAuditReopen).mockResolvedValue(undefined)
  vi.mocked(api.calibreAuditIdentity).mockResolvedValue({ bookId: 5, calibreId: 17, rootKey: 'openlibrary:OL1W', evidence: [
    { status: 'root', workConfidence: 'canonical', editionConfidence: 'unresolved' },
    { status: 'candidate', workConfidence: 'candidate', editionConfidence: 'candidate', editionId: 'OL4M' },
  ], edition: { confidence: 'ambiguous', reasonCode: 'multiple_editions',
    reason: 'Multiple canonical-work editions exist, but no independent or unique edition identifier selects one.', candidates: [
    { provider: 'openlibrary', editionId: 'OL4M', reasons: ['CWA claims isbn (one correlated source)'] },
    { provider: 'openlibrary', editionId: 'OL5M', reasons: [] },
  ] } })
  vi.mocked(api.calibreAuditRecheck).mockResolvedValue({ running: true, startedAt: '2026-01-01' })
  vi.mocked(api.calibreAuditRecheckStatus).mockResolvedValue({ running: false })
  vi.mocked(api.calibreReconcile).mockResolvedValue({ state: 'running', stage: 'ownership', completedStages: [] })
  vi.mocked(api.calibreReconcileStatus).mockResolvedValue({ state: 'idle', completedStages: [] })
  vi.mocked(api.calibreAuditIdentifierProposals).mockResolvedValue({ items: [proposal] })
  vi.mocked(api.calibreAuditIdentifierAdd).mockResolvedValue({ attemptId: 8, outcome: 'added' })
  vi.mocked(api.calibreAuditIdentifierAttempts).mockResolvedValue({ items: [] })
})

describe('CalibreAuditPage', () => {
  it('discloses optional mismatch-tag writes separately from approved identifier additions', async () => {
    renderPage()
    expect(screen.getByText(/checks can update Calibre mismatch tags without per-finding approval/)).toBeInTheDocument()
    expect(screen.getByText(/it does not change other Calibre metadata/)).toBeInTheDocument()
    expect(screen.getByText(/Adding identifiers requires separate approval/)).toBeInTheDocument()
  })

  it('runs reconciliation, shows its stage, disables both starts, and reports completion', async () => {
    vi.mocked(api.calibreReconcileStatus).mockResolvedValueOnce({ state: 'idle', completedStages: [] })
      .mockResolvedValueOnce({ state: 'completed', completedStages: ['ownership', 'identity', 'audit'], result: {
        totalBinderyBooks: 7, totalCalibreBooks: 90, matched: 1, revalidated: 4, stale: 1, unmatched: 1, artifactScansCached: 3,
        identity: { refreshedWorks: 2, evidenceRecords: 6, failedLookups: 0, truncatedLookups: 0, unconfiguredLookups: 0, notAttemptedLookups: 0, deferredWorks: 0, unresolvedRoots: 0, discoveryUnavailable: false },
        audit: { totalCalibreBooks: 90, comparedBooks: 5, findings: 3, updated: 1 },
        transitions: { newUnresolved: 1, resolved: 0, ignoredPreserved: 2, becameHistorical: 0 },
      } })
    renderPage()
    const run = await screen.findByRole('button', { name: 'Refresh book matches and comparisons' })
    await waitFor(() => expect(run).toBeEnabled())
    fireEvent.click(run)
    expect(await screen.findByText(/Running: matching books/)).toBeInTheDocument()
    expect(run).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Recheck library' })).toBeDisabled()
    expect(await screen.findByText(/Books: 7 in Bindery/, {}, { timeout: 3500 })).toBeInTheDocument()
    expect(screen.getByText(/2 books checked, 6 records retrieved/)).toBeInTheDocument()
    expect(screen.getByText(/3 earlier scans reused; no files scanned again/)).toBeInTheDocument()
    expect(screen.getByText(/1 new differences to review, 0 resolved, 2 ignored items kept/)).toBeInTheDocument()
    await waitFor(() => expect(api.calibreAudit).toHaveBeenCalledTimes(2))
  })

  it('shows partial provider outcomes as incomplete discovery, not clean success', async () => {
    vi.mocked(api.calibreReconcileStatus).mockResolvedValue({ state: 'partial', completedStages: ['ownership', 'identity', 'audit'], result: {
      totalBinderyBooks: 1, totalCalibreBooks: 5, matched: 0, revalidated: 1, stale: 0, unmatched: 0, artifactScansCached: 0,
      identity: { refreshedWorks: 1, evidenceRecords: 2, failedLookups: 1, truncatedLookups: 1, unconfiguredLookups: 1, notAttemptedLookups: 0, deferredWorks: 3, unresolvedRoots: 0, discoveryUnavailable: false },
    } })
    renderPage()
    expect(await screen.findByText(/Some provider information could not be retrieved/)).toBeInTheDocument()
    expect(screen.getByText(/1 failed, 1 incomplete, 1 not configured/)).toBeInTheDocument()
  })

  it('restores a failed run and surfaces start errors without a success summary', async () => {
    vi.mocked(api.calibreReconcileStatus).mockResolvedValue({ state: 'failed', completedStages: ['ownership'], error: 'read-only library unavailable' })
    vi.mocked(api.calibreReconcile).mockRejectedValueOnce(new Error('worker unavailable'))
    renderPage()
    expect(await screen.findByText(/Reconciliation failed: read-only library unavailable/)).toBeInTheDocument()
    const run = screen.getByRole('button', { name: 'Refresh book matches and comparisons' })
    await waitFor(() => expect(run).toBeEnabled())
    fireEvent.click(run)
    expect(await screen.findByText(/Could not start reconciliation: worker unavailable/)).toBeInTheDocument()
    expect(screen.queryByText(/Book matches and comparisons refreshed/)).not.toBeInTheDocument()
  })

  it('follows an existing reconciliation on conflict without starting a second job', async () => {
    vi.mocked(api.calibreReconcile).mockRejectedValueOnce(new ApiError(409, { state: 'running' }, 'busy'))
    vi.mocked(api.calibreReconcileStatus).mockResolvedValueOnce({ state: 'idle', completedStages: [] })
      .mockResolvedValueOnce({ state: 'running', stage: 'identity', completedStages: ['ownership'] })
    renderPage()
    const run = await screen.findByRole('button', { name: 'Refresh book matches and comparisons' })
    await waitFor(() => expect(run).toBeEnabled())
    fireEvent.click(run)
    expect(await screen.findByText(/Running: checking metadata providers/)).toBeInTheDocument()
    expect(api.calibreReconcile).toHaveBeenCalledTimes(1)
    expect(run).toBeDisabled()
  })

  it('keeps failed identifier attempts inspectable even when writes are disabled', async () => {
    vi.mocked(api.calibreAuditIdentifierAttempts).mockResolvedValue({ items: [{
      id: 21, actorUserId: 7, identifierType: 'openlibrary', oldValue: '', proposedValue: 'OL12W',
      evidenceKeys: ['root'], action: 'add', outcome: 'failed', error: 'database locked', createdAt: '2026-01-01',
    }] })
    renderPage()
    fireEvent.click(await screen.findByRole('button', { name: 'Identifier write attempts' }))
    expect((await screen.findByRole('link', { name: 'OL12W ↗' })).closest('p')).toHaveTextContent('Request #21: add openlibrary=OL12W ↗ — failed')
    expect(screen.getByText(/Failure: database locked/)).toBeInTheDocument()
    expect(api.calibreAuditIdentifierAttempts).toHaveBeenCalledWith(3)
    expect(screen.queryByRole('button', { name: 'Review proposed identifier' })).not.toBeInTheDocument()
  })

  it('fails closed when the independent write setting is absent, false or unreadable', async () => {
    vi.mocked(api.calibreAudit).mockResolvedValue({ items: [missing], total: 1, limit: 50, offset: 0 })
    for (const value of ['false', 'yes', '']) {
      vi.mocked(api.getSetting).mockImplementation(async key => ({ key, value: key === 'calibre.identifier_write_enabled' ? value : '' }))
      const view = renderPage()
      await screen.findByText('No stored value')
      await waitFor(() => expect(api.getSetting).toHaveBeenCalledWith('calibre.identifier_write_enabled'))
      expect(screen.queryByRole('button', { name: 'Review proposed identifier' })).not.toBeInTheDocument()
      view.unmount()
    }
    vi.mocked(api.getSetting).mockRejectedValue(new Error('setting unavailable'))
    const view = renderPage()
    await screen.findByText('No stored value')
    expect(screen.queryByRole('button', { name: 'Review proposed identifier' })).not.toBeInTheDocument()
    view.unmount()
    expect(api.calibreAuditIdentifierProposals).not.toHaveBeenCalled()
  })

  it('previews provenance and safe source links, selects one addition, and requires explicit approval before apply', async () => {
    vi.mocked(api.getSetting).mockImplementation(async key => ({ key, value: key === 'calibre.identifier_write_enabled' ? 'true' : '' }))
    vi.mocked(api.calibreAudit).mockResolvedValue({ items: [missing], total: 1, limit: 50, offset: 0 })
    vi.mocked(api.calibreAuditIdentifierProposals).mockResolvedValue({ items: [{ ...proposal,
      evidence: [...proposal.evidence, { value: 'unlinked evidence', source: 'books.foreign_id', provider: 'unknown', foreignId: 'javascript:alert(1)' }],
    }, {
      ...proposal, identifierType: 'unknown', proposedValue: 'javascript:alert(1)',
      evidence: [{ value: 'bad', source: 'books.foreign_id', provider: 'unknown', foreignId: 'javascript:alert(1)' }],
    }] })
    renderPage()
    fireEvent.click(await screen.findByRole('button', { name: 'Review proposed identifier' }))
    expect((await screen.findAllByText(/Bindery found this missing book identifier via a metadata provider/)).length).toBeGreaterThan(0)
    expect(api.calibreAuditIdentifierProposals).toHaveBeenCalledWith(3)
    expect(screen.getAllByText('No stored value', { exact: false }).length).toBeGreaterThan(0)
    expect(screen.getAllByRole('link', { name: 'OL12W ↗' }).some(link => link.getAttribute('href') === 'https://openlibrary.org/works/OL12W')).toBe(true)
    expect(screen.queryByRole('link', { name: /javascript/ })).not.toBeInTheDocument()
    const apply = screen.getByRole('button', { name: 'Add selected identifier to Calibre/CWA' })
    expect(apply).toBeDisabled()
    const radio = screen.getByRole('radio', { name: /openlibrary.*OL12W/i })
    fireEvent.click(radio.closest('label')!)
    expect(radio).toBeChecked()
    expect(apply).toBeDisabled()
    fireEvent.click(screen.getByRole('checkbox', { name: /I reviewed.*add only/i }))
    fireEvent.click(apply)
    await waitFor(() => expect(api.calibreAuditIdentifierAdd).toHaveBeenCalledWith(3, { comparisonFingerprint: 'fp1', proposedValue: 'OL12W' }))
    await waitFor(() => expect(api.calibreAudit).toHaveBeenCalledTimes(2))
    expect(await screen.findByRole('status', { name: /Identifier addition/i })).toHaveTextContent(/added/i)
  })

  it('links a server-proposed bare Hardcover slug but never guesses a numeric or unknown source', async () => {
    vi.mocked(api.getSetting).mockImplementation(async key => ({ key, value: key === 'calibre.identifier_write_enabled' ? 'true' : '' }))
    vi.mocked(api.calibreAudit).mockResolvedValue({ items: [{ ...missing, evidenceKey: 'hardcover' }], total: 1, limit: 50, offset: 0 })
    vi.mocked(api.calibreAuditIdentifierProposals).mockResolvedValue({ items: [{ ...proposal,
      identifierType: 'hardcover', proposedValue: 'known-slug',
      evidence: [{ value: '123', source: 'books.foreign_id', provider: 'hardcover', foreignId: 'hc:123' }],
    }] })
    renderPage()
    fireEvent.click(await screen.findByRole('button', { name: 'Review proposed identifier' }))
    const links = await screen.findAllByRole('link', { name: 'known-slug ↗' })
    expect(links.every(link => link.getAttribute('href') === 'https://hardcover.app/books/known-slug')).toBe(true)
    expect(links[0]).toHaveAttribute('rel', 'noopener noreferrer')
    expect(screen.getByText('123').closest('li')).toHaveTextContent('hc:123')
    expect(screen.queryByRole('link', { name: '123 ↗' })).not.toBeInTheDocument()
    expect(screen.queryByRole('link', { name: 'hc:123 ↗' })).not.toBeInTheDocument()
  })

  it('does not offer writes for edition, conflict, historical or ambiguous findings even when enabled', async () => {
    vi.mocked(api.getSetting).mockImplementation(async key => ({ key, value: key === 'calibre.identifier_write_enabled' ? 'true' : '' }))
    vi.mocked(api.calibreAudit).mockResolvedValue({ items: [
      { ...missing, id: 4, evidenceKey: 'isbn' }, { ...missing, id: 5, findingType: 'identifier_conflict' },
      { ...missing, id: 6, state: 'unmatched' }, { ...missing, id: 7, assessment: 'ambiguous' },
    ], total: 4, limit: 50, offset: 0 })
    renderPage()
    await screen.findByText('4 findings match these filters')
    expect(screen.queryByRole('button', { name: 'Review proposed identifier' })).not.toBeInTheDocument()
  })

  it('rejects stale proposals without posting and refreshes after a stale apply', async () => {
    vi.mocked(api.getSetting).mockImplementation(async key => ({ key, value: key === 'calibre.identifier_write_enabled' ? 'true' : '' }))
    vi.mocked(api.calibreAudit).mockResolvedValue({ items: [missing], total: 1, limit: 50, offset: 0 })
    vi.mocked(api.calibreAuditIdentifierProposals).mockResolvedValueOnce({ items: [{ ...proposal, comparisonFingerprint: 'older' }] }).mockResolvedValueOnce({ items: [proposal] })
    vi.mocked(api.calibreAuditIdentifierAdd).mockRejectedValueOnce(new ApiError(409, { error: 'stale' }, 'stale'))
    renderPage()
    const preview = await screen.findByRole('button', { name: 'Review proposed identifier' })
    fireEvent.click(preview)
    expect(await screen.findByRole('alert')).toHaveTextContent(/finding changed/i)
    expect(api.calibreAuditIdentifierAdd).not.toHaveBeenCalled()
    fireEvent.click(preview)
    fireEvent.click(await screen.findByRole('radio', { name: /openlibrary.*OL12W/i }))
    fireEvent.click(screen.getByRole('checkbox', { name: /I reviewed.*add only/i }))
    fireEvent.click(screen.getByRole('button', { name: 'Add selected identifier to Calibre/CWA' }))
    await waitFor(() => expect(api.calibreAudit).toHaveBeenCalledTimes(3))
    expect(screen.getByRole('alert')).toHaveTextContent(/finding changed/i)
    expect(screen.queryByRole('button', { name: 'Add selected identifier to Calibre/CWA' })).not.toBeInTheDocument()
  })

  it('reports a successful write with a failed follow-up re-audit without hiding the failure', async () => {
    vi.mocked(api.getSetting).mockImplementation(async key => ({ key, value: key === 'calibre.identifier_write_enabled' ? 'true' : '' }))
    vi.mocked(api.calibreAudit).mockResolvedValueOnce({ items: [missing], total: 1, limit: 50, offset: 0 })
      .mockResolvedValue({ items: [], total: 0, limit: 50, offset: 0 })
    vi.mocked(api.calibreAuditIdentifierAdd).mockResolvedValue({ attemptId: 9, outcome: 'applied', reauditError: 'reader offline' })
    renderPage()
    fireEvent.click(await screen.findByRole('button', { name: 'Review proposed identifier' }))
    fireEvent.click(await screen.findByRole('radio', { name: /openlibrary.*OL12W/i }))
    fireEvent.click(screen.getByRole('checkbox', { name: /I reviewed.*add only/i }))
    fireEvent.click(screen.getByRole('button', { name: 'Add selected identifier to Calibre/CWA' }))
    expect(await screen.findByRole('alert')).toHaveTextContent(/reader offline/i)
    await waitFor(() => expect(api.calibreAudit).toHaveBeenCalledTimes(2))
    expect(await screen.findByText('No findings match these filters.')).toBeInTheDocument()
    expect(screen.getByRole('status', { name: /Identifier addition/i })).toHaveTextContent(/applied/i)
  })

  it('shows an empty preview without approval and reports unconfirmed writes before retry', async () => {
    vi.mocked(api.getSetting).mockImplementation(async key => ({ key, value: key === 'calibre.identifier_write_enabled' ? 'true' : '' }))
    vi.mocked(api.calibreAudit).mockResolvedValue({ items: [missing], total: 1, limit: 50, offset: 0 })
    vi.mocked(api.calibreAuditIdentifierProposals).mockResolvedValueOnce({ items: [] }).mockResolvedValueOnce({ items: [proposal] })
    vi.mocked(api.calibreAuditIdentifierAdd).mockRejectedValueOnce(new ApiError(503, { error: 'write unconfirmed', attemptId: 21 }, 'write unconfirmed'))
    vi.mocked(api.calibreAuditIdentifierAttempts).mockResolvedValueOnce({ items: [] }).mockResolvedValue({ items: [{
      id: 21, actorUserId: 7, identifierType: 'openlibrary', oldValue: '', proposedValue: 'OL12W',
      evidenceKeys: ['root'], action: 'add', outcome: 'failed', error: 'database locked', createdAt: '2026-01-01',
    }] })
    renderPage()
    fireEvent.click(await screen.findByRole('button', { name: 'Identifier write attempts' }))
    expect(await screen.findByText('No identifier write attempts recorded for this finding.')).toBeInTheDocument()
    const preview = await screen.findByRole('button', { name: 'Review proposed identifier' })
    fireEvent.click(preview)
    expect(await screen.findByText(/Bindery cannot safely suggest an identifier/)).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Add selected identifier to Calibre/CWA' })).not.toBeInTheDocument()
    fireEvent.click(preview)
    fireEvent.click(await screen.findByRole('radio', { name: /openlibrary.*OL12W/i }))
    fireEvent.click(screen.getByRole('checkbox', { name: /I reviewed.*add only/i }))
    fireEvent.click(screen.getByRole('button', { name: 'Add selected identifier to Calibre/CWA' }))
    expect(await screen.findByRole('alert')).toHaveTextContent(/inspect Calibre\/CWA before retrying/i)
    expect(screen.getByRole('alert')).toHaveTextContent(/Write attempt #21/)
    expect((await screen.findByRole('link', { name: 'OL12W ↗' })).closest('p')).toHaveTextContent('Request #21: add openlibrary=OL12W ↗ — failed')
    expect(api.calibreAuditIdentifierAttempts).toHaveBeenCalledTimes(2)
    expect(screen.queryByRole('button', { name: 'Add selected identifier to Calibre/CWA' })).not.toBeInTheDocument()
  })

  it('shows owned and provider evidence separately with source, reason, and ambiguity', async () => {
    renderPage()
    expect(await screen.findByText('Owned title')).toBeInTheDocument()
    expect(screen.getAllByText('Provider title')).toHaveLength(2)
    expect(screen.getByRole('link', { name: 'OL1W ↗' })).toHaveAttribute('href', 'https://openlibrary.org/works/OL1W')
    expect(screen.getByText(/Calibre's title differs/)).toBeInTheDocument()
    expect(screen.getByText(/Bindery is confident this is the same book/)).toBeInTheDocument()
    expect(await screen.findByText(/Edition match needs review/)).toBeInTheDocument()
    expect(screen.getByText(/The owned title differs from the stored external work/)).not.toBeVisible()
    expect(screen.getByText('Current comparison · ambiguous')).toBeInTheDocument()
    expect(screen.getAllByText('Ambiguous evidence')).toHaveLength(2)
    expect(screen.queryByRole('link', { name: /Open in CWA/ })).not.toBeInTheDocument()
  })

  it('ignores with optimistic fingerprint and reloads the queue', async () => {
    renderPage()
    fireEvent.click(await screen.findByRole('button', { name: 'Ignore this comparison' }))
    await waitFor(() => expect(api.calibreAuditIgnore).toHaveBeenCalledWith(3, 'fp1'))
    await waitFor(() => expect(api.calibreAudit).toHaveBeenCalledTimes(2))
  })

  it('reports stale and network action errors rather than pretending ignore succeeded', async () => {
    vi.mocked(api.calibreAuditIgnore).mockRejectedValueOnce(new ApiError(409, { error: 'stale' }, 'stale'))
    renderPage()
    fireEvent.click(await screen.findByRole('button', { name: 'Ignore this comparison' }))
    expect(await screen.findByRole('alert')).toHaveTextContent(/finding changed/i)
    await waitFor(() => expect(api.calibreAudit).toHaveBeenCalledTimes(2))
  })

  it('accepts a background audit, polls for completion and refreshes findings', async () => {
    vi.mocked(api.calibreAuditRecheckStatus)
      .mockResolvedValueOnce({ running: false })
      .mockResolvedValueOnce({ running: false, finishedAt: '2026-01-02', result: { totalCalibreBooks: 80, comparedBooks: 1, findings: 1, updated: 0 } })
    renderPage()
    const button = await screen.findByRole('button', { name: 'Recheck library' })
    await waitFor(() => expect(button).toBeEnabled())
    fireEvent.click(button)
    expect(await screen.findByText(/Recheck accepted/)).toBeInTheDocument()
    expect(screen.queryByText(/Checked 1 matched books/)).not.toBeInTheDocument()
    expect(button).toBeDisabled()
    expect(await screen.findByText(/Checked 1 matched books/, {}, { timeout: 3500 })).toBeInTheDocument()
    await waitFor(() => expect(api.calibreAudit).toHaveBeenCalledTimes(2))
  })

  it('recognizes a concurrent start and follows its status without starting another audit', async () => {
    vi.mocked(api.calibreAuditRecheck).mockRejectedValueOnce(new ApiError(409, { running: true }, 'already running'))
    vi.mocked(api.calibreAuditRecheckStatus).mockResolvedValueOnce({ running: false }).mockResolvedValueOnce({ running: true, startedAt: '2026-01-01' })
    renderPage()
    const button = await screen.findByRole('button', { name: 'Recheck library' })
    await waitFor(() => expect(button).toBeEnabled())
    fireEvent.click(button)
    expect(await screen.findByText(/A recheck is already queued or running/)).toBeInTheDocument()
    expect(button).toBeDisabled()
    expect(api.calibreAuditRecheck).toHaveBeenCalledTimes(1)
  })

  it('restores a running audit after remount and reports its failure without a success summary', async () => {
    vi.mocked(api.calibreAuditRecheckStatus).mockResolvedValueOnce({ running: true, startedAt: '2026-01-01' })
      .mockResolvedValueOnce({ running: false, finishedAt: '2026-01-02', error: 'reader unavailable' })
    renderPage()
    await waitFor(() => expect(screen.getAllByRole('status').some(el => el.textContent === 'Audit queued or running…')).toBe(true))
    expect(screen.getByRole('button', { name: 'Audit queued or running…' })).toBeDisabled()
    expect(await screen.findByRole('alert', {}, { timeout: 3500 })).toHaveTextContent(/reader unavailable/)
    expect(screen.queryByText(/Checked .* matched books/)).not.toBeInTheDocument()
  })

  it('filters on the server and paginates instead of slicing one client page', async () => {
    vi.mocked(api.calibreAudit).mockResolvedValue({ items: [finding], total: 90, limit: 50, offset: 0 })
    renderPage()
    await screen.findByText('Owned title')
    fireEvent.change(screen.getByLabelText('Finding type'), { target: { value: 'title_difference' } })
    await waitFor(() => expect(api.calibreAudit).toHaveBeenCalledWith(expect.objectContaining({ findingType: 'title_difference', state: 'unresolved' })))
    fireEvent.click(screen.getByRole('button', { name: '2' }))
    await waitFor(() => expect(api.calibreAudit).toHaveBeenCalledWith(expect.objectContaining({ limit: 50, offset: 50 })))
  })

  it('distinguishes unmatched history and links to configured CWA only for a safe URL', async () => {
    vi.mocked(api.getSetting).mockResolvedValue({ key: 'cwa.web_url', value: 'https://cwa.example.org/root/' })
    vi.mocked(api.calibreAudit).mockResolvedValue({ items: [{ ...finding, state: 'unmatched' }], total: 1, limit: 50, offset: 0 })
    renderPage()
    expect(await screen.findByText(/historical values/)).toBeInTheDocument()
    expect(screen.getByText('Historical only · not a current mismatch')).toBeInTheDocument()
    expect(screen.getByText('Previously stored in Calibre')).toBeInTheDocument()
    expect(screen.queryByText(/Technical details/)).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Ignore this comparison' })).not.toBeInTheDocument()
    expect(await screen.findByRole('link', { name: /Open in CWA/ })).toHaveAttribute('href', 'https://cwa.example.org/root/book/17')
  })

  it('reopens an ignored finding with the current fingerprint and shows its prior decision', async () => {
    vi.mocked(api.calibreAudit).mockResolvedValue({ items: [{ ...finding, state: 'ignored', comparisonFingerprint: 'changed',
      decisions: [{ action: 'ignore', comparisonFingerprint: 'fp1', createdAt: '2026-01-01' }] }], total: 1, limit: 50, offset: 0 })
    renderPage()
    fireEvent.click(await screen.findByText('Review decision history'))
    expect(screen.getAllByText(/Ignored ·/)).toHaveLength(2)
    fireEvent.click(screen.getByRole('button', { name: 'Reopen for review' }))
    await waitFor(() => expect(api.calibreAuditReopen).toHaveBeenCalledWith(3, 'changed'))
    expect(screen.queryByRole('button', { name: 'Ignore this comparison' })).not.toBeInTheDocument()
  })

  it('treats ASIN as edition-oriented review evidence, not a confirmed owned edition', async () => {
    vi.mocked(api.getSetting).mockImplementation(async key => ({ key, value: key === 'calibre.identifier_write_enabled' ? 'true' : '' }))
    vi.mocked(api.calibreAudit).mockResolvedValue({ items: [{ ...finding, field: 'identifiers', evidenceKey: 'asin',
      findingType: 'identifier_missing', assessment: 'ambiguous' }], total: 1, limit: 50, offset: 0 })
    renderPage()
    expect(await screen.findByText('Possible edition identifier (not a confirmed edition)')).toBeInTheDocument()
    expect(screen.getByText(/This identifier describes an edition, not just a book/)).toBeInTheDocument()
    expect(await screen.findByText('Edition match needs review')).toBeInTheDocument()
    expect(screen.getByText(/Bindery cannot safely choose an edition/)).toBeInTheDocument()
    expect(screen.getByText(/cannot add or replace edition identifiers/)).toBeInTheDocument()
    expect(screen.getByText(/Multiple canonical-work editions exist/)).not.toBeVisible()
    const candidates = screen.getByText('Editions returned by the metadata provider').parentElement!
    expect(within(candidates).getByRole('link', { name: 'OL4M ↗' }).closest('li')).toHaveTextContent('OpenLibrary · OL4M ↗')
    expect(within(candidates).getByRole('link', { name: 'OL5M ↗' }).closest('li')).toHaveTextContent('OpenLibrary · OL5M ↗')
    expect(api.calibreAuditIdentity).toHaveBeenCalledWith(5)
  })

  it('links structured candidate claims in edition details without treating diagnostic text as a source', async () => {
    vi.mocked(api.getSetting).mockImplementation(async key => ({ key, value: key === 'calibre.identifier_write_enabled' ? 'true' : '' }))
    vi.mocked(api.calibreAuditIdentity).mockResolvedValue({ bookId: 5, calibreId: 17, rootKey: 'openlibrary:OL1W', evidence: [
      { status: 'root', workConfidence: 'canonical', editionConfidence: 'unresolved' },
    ], edition: { confidence: 'ambiguous', reasonCode: 'multiple_editions',
      reason: 'Multiple canonical-work editions exist, but no independent or unique edition identifier selects one.', candidates: [
        { provider: 'openlibrary', editionId: 'OL4M', reasons: ['CWA claims (one correlated source)', 'pre-write-back file ISBN matches this edition'], claims: [
          { type: 'openlibrary_edition', value: 'OL4M' }, { type: 'isbn', value: '9780306406157' },
          { type: 'isbn', value: '9780306406158' }, { type: 'unknown', value: '12345' },
        ] },
      ] } })
    renderPage()
    expect(await screen.findByText('Edition match needs review')).toBeInTheDocument()
    expect(screen.getByText(/Bindery cannot safely choose an edition/)).toBeInTheDocument()
    expect(within(screen.getByText('Editions returned by the metadata provider').parentElement!).getByRole('link', { name: 'OL4M ↗' })).toHaveAttribute('href', 'https://openlibrary.org/books/OL4M')
    expect(screen.getByText(/CWA claims \(one correlated source\)/)).not.toBeVisible()
    fireEvent.click(screen.getByText('Edition evidence details'))
    const details = screen.getByText('Edition evidence details').closest('details')!
    const isbn = within(details).getByRole('link', { name: '9780306406157 ↗' })
    expect(isbn).toHaveAttribute('href', 'https://openlibrary.org/isbn/9780306406157')
    expect(isbn).toHaveAttribute('target', '_blank')
    expect(isbn).toHaveAttribute('rel', 'noopener noreferrer')
    expect(within(details).getAllByRole('link', { name: 'OL4M ↗' })).toHaveLength(2)
    expect(isbn.closest('p')).toHaveTextContent('openlibrary_edition: OL4M ↗, isbn: 9780306406157 ↗')
    expect(within(details).queryByRole('link', { name: '9780306406158 ↗' })).not.toBeInTheDocument()
    expect(within(details).queryByRole('link', { name: '12345 ↗' })).not.toBeInTheDocument()
    expect(details).toHaveTextContent('isbn: 9780306406158, unknown: 12345')
    expect(details).toHaveTextContent('CWA claims (one correlated source); pre-write-back file ISBN matches this edition')
    expect(screen.queryByRole('button', { name: 'Review proposed identifier' })).not.toBeInTheDocument()
  })

  it('does not imply uncertainty when an exact edition accompanies an edition-scoped blocked finding', async () => {
    vi.mocked(api.getSetting).mockImplementation(async key => ({ key, value: key === 'calibre.identifier_write_enabled' ? 'true' : '' }))
    vi.mocked(api.calibreAudit).mockResolvedValue({ items: [{ ...finding, field: 'identifiers', evidenceKey: 'asin',
      findingType: 'identifier_missing', assessment: 'ambiguous' }], total: 1, limit: 50, offset: 0 })
    vi.mocked(api.calibreAuditIdentity).mockResolvedValue({ bookId: 5, calibreId: 17, rootKey: 'openlibrary:OL1W', evidence: [],
      edition: { confidence: 'exact', reasonCode: 'independent_file_isbn', editionId: 'OL40M',
        reason: 'A complete pre-write-back artifact ISBN uniquely matches a canonical-work ebook edition.', candidates: [] } })
    renderPage()
    expect(await screen.findByText(/Exact edition identified/)).toBeInTheDocument()
    expect(screen.getByText(/This identifier describes an edition, not just a book/)).toBeInTheDocument()
    expect(screen.getByText(/cannot add or replace edition identifiers, even when an edition has been identified/)).toBeInTheDocument()
    expect(screen.queryByText(/More than one edition may fit this book/)).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Review proposed identifier' })).not.toBeInTheDocument()
  })

  it('shows the selected owned edition without calling a candidate confirmed by evidence status alone', async () => {
    vi.mocked(api.calibreAuditIdentity).mockResolvedValue({ bookId: 5, calibreId: 17, rootKey: 'openlibrary:OL1W', evidence: [
      { status: 'root', workConfidence: 'canonical', editionConfidence: 'unresolved' },
      { status: 'root', workConfidence: 'exact', editionConfidence: 'unresolved', editionId: 'OL4M' },
    ], edition: { confidence: 'high', editionId: 'OL4M', provider: 'openlibrary',
      reason: 'A CWA identifier uniquely matches a canonical-work ebook edition; correlated CWA fields are one claim, not independent corroboration.', candidates: [{ provider: 'openlibrary', editionId: 'OL4M', reasons: [] }] } })
    renderPage()
    expect(await screen.findByText(/This edition is very likely/)).toBeInTheDocument()
    expect(screen.getByText(/evidence points to this edition/)).toBeInTheDocument()
    expect(screen.getAllByRole('link', { name: 'OL4M ↗' }).find(link => !link.closest('details'))).toHaveAttribute('href', 'https://openlibrary.org/books/OL4M')
    expect(await screen.findByText(/A CWA identifier uniquely matches/)).not.toBeVisible()
    expect(screen.queryByText('A candidate edition is not a confirmed match to the owned file.')).not.toBeInTheDocument()
  })

  it('links valid Calibre provider-native claims but never arbitrary identifier text', async () => {
    vi.mocked(api.calibreAudit).mockResolvedValue({ items: [{ ...finding, field: 'identifiers', evidenceKey: 'openlibrary',
      findingType: 'identifier_conflict', calibreEvidence: [
        { value: 'OL12W', source: 'calibre.identifiers.openlibrary' },
        { value: 'OL34M', source: 'calibre.identifiers.openlibrary_edition' },
        { value: 'abc_3', source: 'calibre.identifiers.google' },
        { value: 'hc:123', source: 'calibre.identifiers.hardcover' },
        { value: 'javascript:alert(1)', source: 'calibre.identifiers.google' },
      ] }], total: 1, limit: 50, offset: 0 })
    renderPage()
    expect(await screen.findByRole('link', { name: 'OL12W ↗' })).toHaveAttribute('href', 'https://openlibrary.org/works/OL12W')
    expect(screen.getByRole('link', { name: 'OL34M ↗' })).toHaveAttribute('href', 'https://openlibrary.org/books/OL34M')
    expect(screen.getByRole('link', { name: 'abc_3 ↗' })).toHaveAttribute('href', 'https://books.google.com/books?id=abc_3')
    expect(screen.queryByRole('link', { name: 'hc:123 ↗' })).not.toBeInTheDocument()
    expect(screen.queryByRole('link', { name: /javascript/ })).not.toBeInTheDocument()
  })

  it('links typed CWA and Bindery edition evidence while leaving unsupported and malformed identifiers readable', async () => {
    vi.mocked(api.calibreAudit).mockResolvedValue({ items: [{ ...finding, field: 'identifiers', evidenceKey: 'isbn',
      findingType: 'identifier_conflict', calibreEvidence: [
        { value: '978-0-306-40615-7', source: 'calibre.identifiers.isbn' },
        { value: 'B000FC1BN8', source: 'calibre.identifiers.asin' },
        { value: '12345', source: 'calibre.identifiers.goodreads' },
        { value: '9780306406158', source: 'calibre.identifiers.isbn' },
      ], binderyEvidence: [
        { value: '9780306406157', source: 'editions.isbn_13', provider: 'openlibrary', foreignId: 'OL4M' },
      ],
    }], total: 1, limit: 50, offset: 0 })
    renderPage()
    const cwaISBN = await screen.findByRole('link', { name: '978-0-306-40615-7 ↗' })
    const providerISBN = screen.getByRole('link', { name: '9780306406157 ↗' })
    expect(cwaISBN).toHaveAttribute('href', 'https://openlibrary.org/isbn/9780306406157')
    expect(providerISBN).toHaveAttribute('href', cwaISBN.getAttribute('href'))
    expect(screen.getByRole('link', { name: 'B000FC1BN8 ↗' })).toHaveAttribute('href', 'https://www.amazon.com/dp/B000FC1BN8')
    expect(screen.getByText('12345')).toBeInTheDocument()
    expect(screen.getByText(/ISBN-13 · 9780306406158/)).toBeInTheDocument()
    expect(screen.queryByRole('link', { name: /12345|9780306406158/ })).not.toBeInTheDocument()
    expect(screen.getByText('Current comparison · ambiguous')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Review proposed identifier' })).not.toBeInTheDocument()
  })

  it('only links supported provider records and separates current review from historical filters', async () => {
    vi.mocked(api.calibreAudit).mockResolvedValue({ items: [{ ...finding, field: 'identifiers', evidenceKey: 'hardcover',
      findingType: 'identifier_missing', assessment: 'needs_review', binderyEvidence: [
        { value: 'hc:123', source: 'books.foreign_id', provider: 'hardcover', foreignId: 'hc:123' },
        { value: 'hc:known-slug', source: 'books.foreign_id', provider: 'hardcover', foreignId: 'hc:known-slug' },
        { value: 'wrong', source: 'books.foreign_id', provider: 'openlibrary', foreignId: 'OL42W<script>' },
        { value: 'gb:vol_1', source: 'book_identifiers.foreign_id', provider: 'googlebooks', foreignId: 'gb:vol_1' },
      ] }], total: 1, limit: 50, offset: 0 })
    renderPage()
    expect(await screen.findByText('Book identifier (not an edition)')).toBeInTheDocument()
    expect(screen.getByText('Current comparison · review')).toBeInTheDocument()
    expect(screen.getAllByRole('link', { name: 'hc:known-slug ↗' }).every(link => link.getAttribute('href') === 'https://hardcover.app/books/known-slug')).toBe(true)
    expect(screen.getAllByRole('link', { name: 'gb:vol_1 ↗' }).every(link => link.getAttribute('href') === 'https://books.google.com/books?id=vol_1')).toBe(true)
    expect(screen.queryByRole('link', { name: 'hc:123 ↗' })).not.toBeInTheDocument()
    expect(screen.queryByRole('link', { name: 'OL42W<script> ↗' })).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Ambiguous editions' }))
    await waitFor(() => expect(api.calibreAudit).toHaveBeenCalledWith(expect.objectContaining({ state: 'unresolved', assessment: 'ambiguous', identifierScope: 'edition' })))
    fireEvent.click(screen.getByRole('button', { name: 'Historical only' }))
    await waitFor(() => expect(api.calibreAudit).toHaveBeenCalledWith(expect.objectContaining({ state: 'unmatched', assessment: undefined, identifierScope: undefined })))
  })

  it('shows list and recheck errors and never emits a javascript link', async () => {
    vi.mocked(api.getSetting).mockResolvedValue({ key: 'cwa.web_url', value: 'javascript:alert(1)' })
    vi.mocked(api.calibreAudit).mockRejectedValueOnce(new Error('database unavailable'))
    vi.mocked(api.calibreAuditRecheck).mockRejectedValueOnce(new Error('library unavailable'))
    renderPage()
    expect(await screen.findByRole('alert')).toHaveTextContent(/database unavailable/)
    const button = screen.getByRole('button', { name: 'Recheck library' })
    await waitFor(() => expect(button).toBeEnabled())
    fireEvent.click(button)
    await waitFor(() => expect(screen.getAllByRole('alert').some(el => el.textContent?.includes('library unavailable'))).toBe(true))
    expect(screen.queryByRole('link', { name: /Open in CWA/ })).not.toBeInTheDocument()
  })

  it('explains an exact edition using independently attested ebook evidence', async () => {
    const snapshot: CalibreIdentitySnapshot = {
      bookId: 5, calibreId: 17, rootKey: 'openlibrary:OL1W', evidence: [],
      artifacts: [{ lineage: 'pre_writeback', attestedOriginal: true, stale: false, historical: false,
        outcome: 'scanned', identifiers: [{ status: 'matches_work', normalizedValue: '9781234567897' }] }],
      edition: { confidence: 'exact', editionId: 'OL4M', provider: 'openlibrary',
        reasonCode: 'independent_file_isbn',
        reason: 'A complete pre-write-back artifact ISBN uniquely matches a canonical-work ebook edition.', candidates: [] },
    }
    vi.mocked(api.calibreAuditIdentity).mockResolvedValue(snapshot)
    renderPage()
    expect(await screen.findByText(/Exact edition identified/)).toHaveTextContent('OL4M')
    expect(screen.getByText(/ISBN was found in data contained in the book file before any known Calibre metadata write-back/)).toBeInTheDocument()
    expect(screen.getByText(/pre-write-back artifact ISBN uniquely matches/)).not.toBeVisible()
  })

  it('explains a likely edition based on a Calibre identifier without treating it as independent', async () => {
    vi.mocked(api.calibreAuditIdentity).mockResolvedValue({
      bookId: 5, calibreId: 17, rootKey: 'openlibrary:OL1W', evidence: [],
      claims: [{ identifierType: 'isbn', status: 'agrees' }],
      edition: { confidence: 'high', editionId: 'OL4M', provider: 'openlibrary',
        reasonCode: 'calibre_identifiers',
        reason: 'A CWA identifier uniquely matches a canonical-work ebook edition.', candidates: [] },
    })
    renderPage()
    expect(await screen.findByText(/This edition is very likely/)).toBeInTheDocument()
    expect(screen.getByText(/An identifier stored in Calibre points to this edition/)).toBeInTheDocument()
    expect(screen.getByText(/CWA identifier uniquely matches/)).not.toBeVisible()
  })

  it('does not count a possibly Calibre-derived ebook identifier as separate confirmation', async () => {
    vi.mocked(api.calibreAuditIdentity).mockResolvedValue({
      bookId: 5, calibreId: 17, rootKey: 'openlibrary:OL1W', evidence: [],
      artifacts: [{ lineage: 'potentially_cwa_derived', attestedOriginal: false, stale: false, historical: false,
        outcome: 'scanned', identifiers: [{ status: 'matches_work', normalizedValue: '9781234567897' }] }],
      edition: { confidence: 'high', editionId: 'OL4M', provider: 'openlibrary', reasonCode: 'calibre_identifiers', artifactWarning: 'possibly_calibre_derived',
        reason: 'A CWA identifier uniquely matches a canonical-work ebook edition; correlated CWA fields are one claim, not independent corroboration.', candidates: [] },
    })
    renderPage()
    expect(await screen.findByText(/may have been written there by Calibre/)).toBeInTheDocument()
    expect(screen.getByText(/This edition is very likely/)).toBeInTheDocument()
    expect(screen.queryByText(/ISBN was found in data contained in the book file before any known/)).not.toBeInTheDocument()
  })

  it('explains incomplete provider results without turning visible candidates into a definite match', async () => {
    vi.mocked(api.calibreAuditIdentity).mockResolvedValue({
      bookId: 5, calibreId: 17, rootKey: 'openlibrary:OL1W',
      evidence: [{ status: 'root', workConfidence: 'exact', editionConfidence: 'candidate',
        provider: 'openlibrary', editionId: 'OL4M', providerMetadata: { title: 'Provider edition title' } }],
      lookups: [{ method: 'exact_editions', outcome: 'truncated', provider: 'openlibrary' }],
      edition: { confidence: 'ambiguous', reasonCode: 'lookup_incomplete', reason: 'The canonical provider exact-editions lookup is incomplete.',
        candidates: [{ provider: 'openlibrary', editionId: 'OL4M', reasons: ['CWA claims isbn (one correlated source)'] }] },
    })
    renderPage()
    expect(await screen.findByText(/provider may have more editions than Bindery received/)).toBeInTheDocument()
    expect(screen.getByText('Edition match needs review')).toBeInTheDocument()
    expect(screen.queryByText(/More than one edition still fits/)).not.toBeInTheDocument()
    expect(within(screen.getByText('Editions returned by the metadata provider').parentElement!).getByRole('link', { name: 'OL4M ↗' }).closest('li')).toHaveTextContent('Provider edition title · OL4M ↗')
    expect(screen.getByText(/not confirmed matches to your book file/)).toBeInTheDocument()
    expect(screen.getByText(/exact-editions lookup is incomplete/)).not.toBeVisible()
    expect(screen.getByText(/CWA claims isbn/)).not.toBeVisible()
  })

  it('distinguishes conflicting edition evidence from a merely unresolved edition', async () => {
    vi.mocked(api.calibreAuditIdentity).mockResolvedValueOnce({
      bookId: 5, calibreId: 17, rootKey: 'openlibrary:OL1W', evidence: [],
      claims: [{ identifierType: 'isbn', status: 'conflicts' }],
      edition: { confidence: 'ambiguous', reasonCode: 'conflicting_evidence', reason: 'Conflicting or multiple edition identifiers remain within the canonical work.', candidates: [] },
    })
    const first = renderPage()
    expect(await screen.findByText(/edition information points to different possibilities or conflicts/)).toBeInTheDocument()
    first.unmount()
    vi.mocked(api.calibreAuditIdentity).mockResolvedValue({
      bookId: 5, calibreId: 17, rootKey: 'openlibrary:OL1W', evidence: [],
      edition: { confidence: 'unresolved', reason: 'No eligible edition of the canonical work has identifying evidence.', candidates: [] },
    })
    renderPage()
    expect(await screen.findByText('Exact edition not identified')).toBeInTheDocument()
    expect(screen.getByText(/does not have enough information to identify/)).toBeInTheDocument()
    expect(screen.queryByText(/edition information points to different possibilities or conflicts/)).not.toBeInTheDocument()
  })

  it('keeps blocked identifier actions explanatory without offering a write', async () => {
    vi.mocked(api.getSetting).mockImplementation(async key => ({ key, value: key === 'calibre.identifier_write_enabled' ? 'true' : '' }))
    vi.mocked(api.calibreAudit).mockResolvedValue({ items: [{ ...missing, evidenceKey: 'isbn', assessment: 'ambiguous' }], total: 1, limit: 50, offset: 0 })
    renderPage()
    expect(await screen.findByText(/cannot add or replace edition identifiers/)).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Review proposed identifier' })).not.toBeInTheDocument()
    expect(api.calibreAuditIdentifierProposals).not.toHaveBeenCalled()
  })

  it('keeps ignored comparisons reopenable and historical values separate from current matches', async () => {
    vi.mocked(api.calibreAudit).mockResolvedValue({ items: [{ ...finding, state: 'unmatched', id: 3 },
      { ...finding, state: 'ignored', id: 4 }], total: 2, limit: 50, offset: 0 })
    renderPage()
    expect(await screen.findByText('Previously stored in Calibre')).toBeInTheDocument()
    expect(screen.getByText(/historical values, not a confirmed current mismatch/)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Reopen for review' })).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Review proposed identifier' })).not.toBeInTheDocument()
    await waitFor(() => expect(api.calibreAuditIdentity).toHaveBeenCalledTimes(1))
  })

  it('renders the edition context once for multiple comparisons about the same book', async () => {
    vi.mocked(api.calibreAudit).mockResolvedValue({ items: [finding, { ...finding, id: 4,
      findingType: 'language_difference', field: 'language', comparisonFingerprint: 'fp2' }], total: 2, limit: 50, offset: 0 })
    renderPage()
    expect(await screen.findAllByText('Edition match needs review')).toHaveLength(1)
    expect(screen.getAllByRole('heading', { level: 3, name: /Provider title/ })).toHaveLength(1)
    expect(api.calibreAuditIdentity).toHaveBeenCalledTimes(1)
  })

  it('groups two work-ID comparisons under one book and cover without mixing their evidence or actions', async () => {
    vi.mocked(api.getSetting).mockImplementation(async key => ({ key, value: key === 'calibre.identifier_write_enabled' ? 'true' : '' }))
    const openlibrary = { ...missing, id: 10, comparisonFingerprint: 'ol-fp',
      calibreEvidence: [{ value: '', source: 'calibre.identifiers.openlibrary' }], binderyEvidence: [
      { value: 'OL81608W', source: 'books.foreign_id', provider: 'openlibrary', foreignId: 'OL81608W' },
    ] }
    const hardcover = { ...missing, id: 11, evidenceKey: 'hardcover', comparisonFingerprint: 'hc-fp', binderyEvidence: [
      { value: 'hc:night-shift', source: 'calibre_identity_evidence.exact_book', provider: 'hardcover', foreignId: 'hc:night-shift' },
    ] }
    vi.mocked(api.calibreAudit).mockResolvedValue({ items: [openlibrary, hardcover], total: 2, limit: 50, offset: 0 })
    vi.mocked(api.calibreAuditIdentity).mockResolvedValue({ bookId: 5, calibreId: 17, rootKey: 'openlibrary:OL1W', hasOwnedCover: true,
      evidence: [{ status: 'root', method: 'exact_editions', canonicalIdentity: 'openlibrary:OL1W', provider: 'openlibrary',
        editionId: 'OL4M', workConfidence: 'exact', editionConfidence: 'unresolved', providerMetadata: {
          ebook: true, imageUrl: 'https://covers.openlibrary.org/b/id/1-L.jpg',
        } }],
      edition: { confidence: 'high', editionId: 'OL4M', provider: 'openlibrary', reasonCode: 'calibre_identifiers',
        reason: 'A Calibre ISBN supports this edition.', candidates: [{ provider: 'openlibrary', editionId: 'OL4M', reasons: [] }] } })
    vi.mocked(api.calibreAuditIdentifierProposals).mockImplementation(async id => ({ items: [
      { ...proposal, findingId: id, comparisonFingerprint: id === 10 ? 'ol-fp' : 'hc-fp',
        identifierType: id === 10 ? 'openlibrary' : 'hardcover', proposedValue: id === 10 ? 'OL81608W' : 'hc:night-shift' },
    ] }))
    renderPage()
    expect(await screen.findByRole('img', { name: 'Cover stored with the current Calibre/CWA book' })).toBeInTheDocument()
    expect(screen.getAllByRole('heading', { level: 3, name: /Provider title/ })).toHaveLength(1)
    const title = screen.getByRole('heading', { level: 3, name: /Provider title/ })
    const editionHeading = screen.getByRole('heading', { level: 4, name: 'Edition match' })
    expect(title).toHaveClass('text-lg', 'font-semibold')
    expect(within(title).getByText('Bindery matched this book to')).toHaveClass('text-xs', 'text-fg-muted')
    expect(editionHeading).toHaveClass('text-base', 'font-semibold', 'border-b')
    expect(screen.getAllByRole('img', { name: 'Cover stored with the current Calibre/CWA book' })).toHaveLength(1)
    expect(screen.getAllByRole('img', { name: 'OpenLibrary edition OL4M cover' })).toHaveLength(1)
    const book = title.closest('li')!
    const comparisons = within(book).getAllByRole('listitem').filter(item => item.querySelector('h4')?.textContent?.startsWith('Identifier comparison:'))
    expect(comparisons).toHaveLength(2)
    for (const comparison of comparisons) {
      const heading = within(comparison).getByRole('heading', { level: 4, name: /Identifier comparison:/ })
      expect(heading).toHaveClass('text-sm', 'font-semibold', 'border-l-2')
      expect(within(comparison).getByRole('heading', { level: 5, name: 'Calibre currently stores' })).toHaveClass('font-medium')
      expect(within(comparison).getByRole('heading', { level: 5, name: 'Bindery found' })).toHaveClass('font-medium')
    }
    expect(comparisons[0]).toHaveTextContent('Identifier comparison: OpenLibrary work ID')
    expect(comparisons[0]).toHaveTextContent('OL81608W')
    expect(comparisons[0]).toHaveTextContent('From a stored metadata record')
    expect(comparisons[0]).not.toHaveTextContent('hc:night-shift')
    expect(comparisons[1]).toHaveTextContent('Identifier comparison: Hardcover book ID')
    expect(comparisons[1]).toHaveTextContent('hc:night-shift')
    expect(comparisons[1]).toHaveTextContent('Found via metadata provider')
    expect(comparisons[1]).not.toHaveTextContent('OL81608W')
    for (const comparison of comparisons) {
      const noValue = within(comparison).getByText('No stored value')
      expect(noValue.closest('li')).not.toHaveTextContent('Stored in Calibre')
      expect(within(comparison).getByRole('button', { name: 'Review proposed identifier' })).toBeEnabled()
    }
    fireEvent.click(within(comparisons[0]).getByRole('button', { name: 'Review proposed identifier' }))
    fireEvent.click(within(comparisons[1]).getByRole('button', { name: 'Review proposed identifier' }))
    await waitFor(() => expect(api.calibreAuditIdentifierProposals).toHaveBeenCalledWith(10))
    await waitFor(() => expect(api.calibreAuditIdentifierProposals).toHaveBeenCalledWith(11))
    expect(api.calibreAuditIdentifierAdd).not.toHaveBeenCalled()
    expect(api.calibreAuditIdentity).toHaveBeenCalledTimes(1)
  })

  it('keeps each book context independent and never gives a historical finding a current edition', async () => {
    vi.mocked(api.calibreAudit).mockResolvedValue({ items: [
      missing,
      { ...missing, id: 4, bookId: 6, calibreId: 18, bookTitle: 'Another book', evidenceKey: 'hardcover', comparisonFingerprint: 'fp2' },
      { ...missing, id: 5, state: 'unmatched', evidenceKey: 'isbn', comparisonFingerprint: 'old-fp' },
    ], total: 3, limit: 50, offset: 0 })
    vi.mocked(api.calibreAuditIdentity).mockImplementation(async bookId => ({ bookId, calibreId: bookId === 5 ? 17 : 18,
      rootKey: 'openlibrary:OL1W', hasOwnedCover: true, evidence: [],
      edition: { confidence: bookId === 5 ? 'high' : 'unresolved', reason: 'Book-specific resolution.', candidates: [] },
    }))
    renderPage()
    expect(await screen.findAllByRole('img', { name: 'Cover stored with the current Calibre/CWA book' })).toHaveLength(2)
    const current = screen.getAllByRole('heading', { level: 3, name: /Provider title/ }).find(h => h.closest('li')?.textContent?.includes('Edition match'))!.closest('li')!
    const another = screen.getByRole('heading', { level: 3, name: /Another book/ }).closest('li')!
    const historical = screen.getAllByRole('heading', { level: 3, name: /Provider title/ }).find(h => !h.closest('li')?.textContent?.includes('Edition match'))!.closest('li')!
    expect(current).toHaveTextContent('This edition is very likely')
    expect(another).toHaveTextContent('Exact edition not identified')
    expect(within(current).getByRole('img', { name: 'Cover stored with the current Calibre/CWA book' })).toHaveAttribute('src', '/api/v1/calibre/identity/5/cover')
    expect(within(another).getByRole('img', { name: 'Cover stored with the current Calibre/CWA book' })).toHaveAttribute('src', '/api/v1/calibre/identity/6/cover')
    expect(historical).toHaveTextContent('Historical only · not a current mismatch')
    expect(historical).not.toHaveTextContent('Edition match')
    expect(within(historical).queryByRole('img')).not.toBeInTheDocument()
    expect(api.calibreAuditIdentity).toHaveBeenCalledTimes(2)
  })

  it('never infers edition basis from simultaneous raw lookup, claim, and file observations', async () => {
    const evidence = [{ status: 'root', workConfidence: 'exact', editionConfidence: 'candidate' }]
    const artifacts = [{ lineage: 'pre_writeback', attestedOriginal: true, stale: false, historical: false,
      outcome: 'scanned', identifiers: [{ status: 'conflict', normalizedValue: '9781234567897' }] }]
    const base = { bookId: 5, calibreId: 17, rootKey: 'openlibrary:OL1W', evidence, artifacts,
      claims: [{ identifierType: 'isbn', status: 'agrees' }],
      lookups: [{ method: 'exact_editions', outcome: 'truncated', provider: 'openlibrary' }] }
    vi.mocked(api.calibreAuditIdentity).mockResolvedValueOnce({ ...base,
      edition: { confidence: 'ambiguous', reasonCode: 'lookup_incomplete',
        reason: 'The canonical provider exact-editions lookup is incomplete.', candidates: [] } })
    const first = renderPage()
    expect(await screen.findByText(/provider may have more editions than Bindery received/)).toBeInTheDocument()
    expect(screen.queryByText(/edition information points to different possibilities or conflicts/)).not.toBeInTheDocument()
    first.unmount()
    vi.mocked(api.calibreAuditIdentity).mockResolvedValueOnce({ ...base,
      edition: { confidence: 'ambiguous', reasonCode: 'conflicting_evidence',
        reason: 'Conflicting or multiple edition identifiers remain within the canonical work.', candidates: [] } })
    const second = renderPage()
    expect(await screen.findByText(/edition information points to different possibilities or conflicts/)).toBeInTheDocument()
    expect(screen.queryByText(/provider may have more editions than Bindery received/)).not.toBeInTheDocument()
    second.unmount()
    vi.mocked(api.calibreAuditIdentity).mockResolvedValue({ ...base,
      edition: { confidence: 'ambiguous', reason: 'Opaque diagnostic without a category.', candidates: [] } })
    renderPage()
    expect(await screen.findByText(/Bindery cannot safely choose an edition/)).toBeInTheDocument()
    expect(screen.queryByText(/provider may have more editions than Bindery received/)).not.toBeInTheDocument()
  })

  it('reloads edition evidence after a refresh that keeps the same finding fingerprint', async () => {
    const first = { bookId: 5, calibreId: 17, rootKey: 'openlibrary:OL1W', evidence: [],
      edition: { confidence: 'ambiguous' as const, reasonCode: 'multiple_editions',
        reason: 'Multiple canonical-work editions exist, but no independent or unique edition identifier selects one.', candidates: [] } }
    vi.mocked(api.calibreAuditIdentity).mockResolvedValueOnce(first).mockResolvedValueOnce({ ...first,
      edition: { confidence: 'exact', reasonCode: 'independent_file_isbn', editionId: 'OL40M',
        reason: 'A complete pre-write-back artifact ISBN uniquely matches a canonical-work ebook edition.', candidates: [] } })
    renderPage()
    expect(await screen.findByText('Edition match needs review')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Ignore this comparison' }))
    await waitFor(() => expect(api.calibreAudit).toHaveBeenCalledTimes(2))
    expect(await screen.findByText(/Exact edition identified/)).toBeInTheDocument()
    expect(api.calibreAuditIdentity).toHaveBeenCalledTimes(2)
  })

  it('does not call a raw original-file observation the resolution basis without its category', async () => {
    const base = { bookId: 5, calibreId: 17, rootKey: 'openlibrary:OL1W', evidence: [],
      artifacts: [{ lineage: 'pre_writeback', attestedOriginal: true, stale: false, historical: false,
        outcome: 'scanned', identifiers: [{ status: 'matches_work', normalizedValue: '9781234567897' }] }] }
    vi.mocked(api.calibreAuditIdentity).mockResolvedValueOnce({ ...base,
      edition: { confidence: 'exact', reason: 'A complete pre-write-back artifact ISBN uniquely matches a canonical-work ebook edition.', candidates: [] } })
    const first = renderPage()
    expect(await screen.findByText('Exact edition identified')).toBeInTheDocument()
    expect(screen.getByText(/An edition was identified from the available evidence/)).toBeInTheDocument()
    expect(screen.getByText(/The source is not specified in this snapshot/)).toBeInTheDocument()
    expect(screen.queryByText(/ISBN was found in data contained in the book file before any known/)).not.toBeInTheDocument()
    first.unmount()
    vi.mocked(api.calibreAuditIdentity).mockResolvedValue({ ...base,
      edition: { confidence: 'high', reasonCode: 'calibre_identifiers',
        reason: 'A CWA identifier uniquely matches a canonical-work ebook edition.', candidates: [] } })
    renderPage()
    expect(await screen.findByText(/An identifier stored in Calibre points to this edition/)).toBeInTheDocument()
    expect(screen.queryByText(/ISBN was found in data contained in the book file before any known/)).not.toBeInTheDocument()
  })

  it('does not call a Calibre claim the resolution basis when the backend chose historical file support', async () => {
    vi.mocked(api.calibreAuditIdentity).mockResolvedValue({ bookId: 5, calibreId: 17,
      rootKey: 'openlibrary:OL1W', evidence: [], claims: [{ identifierType: 'isbn', status: 'agrees' }],
      edition: { confidence: 'high', reasonCode: 'historical_file_isbn',
        reason: 'A historical pre-write-back observation and the current file agree on an ISBN; changed file bytes prevent an exact physical-edition assertion.', candidates: [] } })
    renderPage()
    expect(await screen.findByText(/previous scan and the current book file share an ISBN/)).toBeInTheDocument()
    expect(screen.queryByText(/An identifier stored in Calibre points to this edition/)).not.toBeInTheDocument()
  })

  it('shows two distinct rooted edition covers beside their own IDs and the separate current CWA cover', async () => {
    vi.mocked(api.getSetting).mockImplementation(async key => ({ key, value: key === 'cwa.web_url' ? 'https://cwa.example/root' : 'true' }))
    vi.mocked(api.calibreAudit).mockResolvedValue({ items: [{ ...missing, assessment: 'ambiguous', evidenceKey: 'isbn',
      calibreEvidence: [{ value: '9780306406157', source: 'calibre.identifiers.isbn' }] }], total: 1, limit: 50, offset: 0 })
    vi.mocked(api.calibreAuditIdentity).mockResolvedValue({ bookId: 5, calibreId: 17, rootKey: 'openlibrary:OL1W', hasOwnedCover: true,
      evidence: ['OL4M', 'OL5M'].map((id, index) => ({ status: 'root', method: 'exact_editions', canonicalIdentity: 'openlibrary:OL1W',
        provider: 'openlibrary', editionId: id, workConfidence: 'exact', editionConfidence: 'unresolved',
        providerMetadata: { ebook: true, title: `Edition ${index + 1}`, publisher: `Publisher ${index + 1}`,
          publicationDate: `202${index}-01-01`, imageUrl: `https://covers.openlibrary.org/b/id/${index + 1}-L.jpg` } })),
      edition: { confidence: 'ambiguous', reason: 'Several editions', candidates: [
        { provider: 'openlibrary', editionId: 'OL4M', reasons: [] }, { provider: 'openlibrary', editionId: 'OL5M', reasons: [] },
      ] },
    })
    renderPage()
    const current = await screen.findByRole('img', { name: 'Cover stored with the current Calibre/CWA book' })
    expect(current).toHaveAttribute('src', '/api/v1/calibre/identity/5/cover')
    expect(current).toHaveAttribute('loading', 'lazy')
    const first = screen.getByRole('img', { name: 'OpenLibrary edition OL4M cover' })
    const second = screen.getByRole('img', { name: 'OpenLibrary edition OL5M cover' })
    expect(first.getAttribute('src')).toContain(encodeURIComponent('https://covers.openlibrary.org/b/id/1-L.jpg'))
    expect(second.getAttribute('src')).toContain(encodeURIComponent('https://covers.openlibrary.org/b/id/2-L.jpg'))
    for (const [image, id] of [[first, 'OL4M'], [second, 'OL5M']] as const) {
      const card = image.closest('li')!
      expect(within(card).getByRole('link', { name: `${id} ↗` })).toHaveAttribute('href', `https://openlibrary.org/books/${id}`)
      expect(within(card).getByRole('link', { name: `${id} ↗` })).toHaveAttribute('target', '_blank')
      expect(within(card).getByRole('link', { name: `${id} ↗` })).toHaveAttribute('rel', 'noopener noreferrer')
    }
    expect(screen.getByText('Publisher 1')).toBeInTheDocument()
    expect(screen.getByText('Current Calibre/CWA book (owned copy) · Calibre book #17')).toBeInTheDocument()
    expect(screen.getByText(/Covers are for visual comparison only/)).toHaveTextContent('They do not confirm the edition.')
    expect(screen.getByText('Edition match needs review')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Review proposed identifier' })).not.toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Open in CWA ↗' })).toHaveAttribute('href', 'https://cwa.example/root/book/17')
  })

  it('keeps edition navigation and CWA claims when covers are missing or fail to load', async () => {
    vi.mocked(api.calibreAuditIdentity).mockResolvedValue({ bookId: 5, calibreId: 17, rootKey: 'openlibrary:OL1W', hasOwnedCover: true,
      evidence: [{ status: 'root', method: 'exact_editions', canonicalIdentity: 'openlibrary:OL1W', provider: 'openlibrary',
        editionId: 'OL4M', workConfidence: 'exact', editionConfidence: 'unresolved', providerMetadata: { ebook: true,
          imageUrl: 'https://covers.openlibrary.org/b/id/1-L.jpg' } }],
      edition: { confidence: 'ambiguous', reason: 'Several editions', candidates: [
        { provider: 'openlibrary', editionId: 'OL4M', reasons: [] }, { provider: 'openlibrary', editionId: 'OL5M', reasons: [] },
      ] },
    })
    renderPage()
    const candidate = await screen.findByRole('img', { name: 'OpenLibrary edition OL4M cover' })
    fireEvent.error(candidate)
    fireEvent.error(screen.getByRole('img', { name: 'Cover stored with the current Calibre/CWA book' }))
    expect(screen.queryByRole('img')).not.toBeInTheDocument()
    expect(screen.getAllByRole('link', { name: 'OL4M ↗' }).every(link => link.getAttribute('href') === 'https://openlibrary.org/books/OL4M')).toBe(true)
    expect(screen.getAllByRole('link', { name: 'OL5M ↗' }).every(link => link.getAttribute('href') === 'https://openlibrary.org/books/OL5M')).toBe(true)
    expect(screen.getByText('Owned title')).toBeInTheDocument()
    expect(screen.getByText('Edition match needs review')).toBeInTheDocument()
  })

  it('refuses unsafe, unrooted or conflicting artwork without suppressing candidate IDs', async () => {
    const raw = ['javascript:alert(1)', 'http://insecure.example/image.jpg', 'https://user:pass@example.org/a.jpg',
      'https://example.org/a.jpg#fragment', 'https://example.org/other.jpg']
    vi.mocked(api.calibreAuditIdentity).mockResolvedValue({ bookId: 5, calibreId: 17, rootKey: 'openlibrary:OL1W', evidence: [
      ...raw.map((imageUrl, i) => ({ status: 'root', method: 'exact_editions', canonicalIdentity: 'openlibrary:OL1W',
        provider: 'openlibrary', editionId: `OL${i + 4}M`, workConfidence: 'exact', editionConfidence: 'unresolved',
        providerMetadata: { ebook: true, imageUrl } })),
      { status: 'candidate', method: 'exact_editions', canonicalIdentity: 'openlibrary:OL1W', provider: 'openlibrary',
        editionId: 'OL9M', workConfidence: 'low', editionConfidence: 'unresolved', providerMetadata: { ebook: true, imageUrl: 'https://example.org/unrooted.jpg' } },
      { status: 'root', method: 'exact_editions', canonicalIdentity: 'another-work', provider: 'openlibrary',
        editionId: 'OL10M', workConfidence: 'exact', editionConfidence: 'unresolved', providerMetadata: { ebook: true, imageUrl: 'https://example.org/wrong.jpg' } },
      ...[1, 2].map(i => ({ status: 'root', method: 'exact_editions', canonicalIdentity: 'openlibrary:OL1W',
        provider: 'openlibrary', editionId: 'OL11M', workConfidence: 'exact', editionConfidence: 'unresolved',
        providerMetadata: { ebook: true, imageUrl: `https://example.org/${i}.jpg` } })),
    ], edition: { confidence: 'unresolved', reason: 'No winner', candidates: Array.from({ length: 8 }, (_, i) =>
      ({ provider: 'openlibrary', editionId: `OL${i + 4}M`, reasons: [] })) } })
    renderPage()
    expect(await screen.findByText('Exact edition not identified')).toBeInTheDocument()
    expect(screen.getAllByRole('img')).toHaveLength(1) // only OL8M has an unambiguous, HTTPS source
    expect(screen.getByRole('img')).toHaveAttribute('alt', 'OpenLibrary edition OL8M cover')
    for (let i = 4; i <= 11; i++) expect(screen.getAllByRole('link', { name: `OL${i}M ↗` }).every(link => link.getAttribute('rel') === 'noopener noreferrer')).toBe(true)
    expect(screen.queryByRole('button', { name: 'Review proposed identifier' })).not.toBeInTheDocument()
  })

  it('uses the same identifier-add gate for blocked copy and visible action across overlapping reasons', async () => {
    const scenarios: Array<{ name: string; enabled: boolean; item: CalibreAuditFinding; explanation: RegExp }> = [
      { name: 'disabled even on a conflict', enabled: false, item: { ...missing, findingType: 'identifier_conflict', assessment: 'ambiguous', evidenceKey: 'isbn' }, explanation: /Adding identifiers is turned off/ },
      { name: 'conflict with ambiguous edition scope', enabled: true, item: { ...missing, findingType: 'identifier_conflict', assessment: 'ambiguous', evidenceKey: 'isbn' }, explanation: /cannot replace a conflicting identifier/ },
      { name: 'ambiguous work identifier', enabled: true, item: { ...missing, assessment: 'ambiguous' }, explanation: /does not support one safe identifier to add/ },
      { name: 'edition identifier even with needs-review assessment', enabled: true, item: { ...missing, evidenceKey: 'isbn' }, explanation: /cannot add or replace edition identifiers/ },
      { name: 'unsupported identifier', enabled: true, item: { ...missing, evidenceKey: 'unsupported' }, explanation: /cannot be added from this page/ },
    ]
    for (const scenario of scenarios) {
      vi.mocked(api.getSetting).mockImplementation(async key => ({ key, value: key === 'calibre.identifier_write_enabled' && scenario.enabled ? 'true' : '' }))
      vi.mocked(api.calibreAudit).mockResolvedValue({ items: [scenario.item], total: 1, limit: 50, offset: 0 })
      const view = renderPage()
      expect(await screen.findByText(scenario.explanation), scenario.name).toBeInTheDocument()
      expect(screen.queryByRole('button', { name: 'Review proposed identifier' }), scenario.name).not.toBeInTheDocument()
      view.unmount()
    }
    expect(api.calibreAuditIdentifierProposals).not.toHaveBeenCalled()
  })

  it('retires stored provider record in identifier diagnostic details without changing finding reasons', async () => {
    const missingReason = 'Calibre has no identifier of this type, but the stored provider record does.'
    const conflictReason = 'Calibre and the stored provider record use different identifiers of this type; editions may differ.'
    const items = [
      { ...missing, id: 20, reason: missingReason },
      { ...missing, id: 21, evidenceKey: 'hardcover', findingType: 'identifier_conflict' as const, reason: conflictReason },
    ]
    vi.mocked(api.calibreAudit).mockResolvedValue({ items, total: 2, limit: 50, offset: 0 })
    renderPage()
    for (const [type, expected] of [
      ['OpenLibrary work ID', 'Calibre has no identifier of this type, but the stored metadata record does.'],
      ['Hardcover book ID', 'Calibre and the stored metadata record use different identifiers of this type; editions may differ.'],
    ]) {
      const comparison = (await screen.findByRole('heading', { level: 4, name: `Identifier comparison: ${type}` })).closest('li')!
      fireEvent.click(within(comparison).getByText('Technical details'))
      const details = within(comparison).getByText(`Diagnostic reason: ${expected}`)
      expect(details).toBeVisible()
      expect(details).not.toHaveTextContent('stored provider record')
    }
    expect(items.map(item => item.reason)).toEqual([missingReason, conflictReason])
  })

  it('shows repeated Hardcover, OpenLibrary and ISBN claims once per literal value without discarding provenance', async () => {
    vi.mocked(api.calibreAudit).mockResolvedValue({ items: [
      { ...missing, id: 10, evidenceKey: 'hardcover', binderyEvidence: [
        { value: 'hc:night-shift', source: 'books.foreign_id', provider: 'hardcover', foreignId: 'hc:night-shift', recordId: 1 },
        { value: 'hc:night-shift', source: 'books.foreign_id', provider: 'hardcover', foreignId: 'hc:night-shift', recordId: 2 },
        { value: 'hc:night-shift', source: 'calibre_identity_evidence.exact_book', provider: 'hardcover', foreignId: 'hc:night-shift' },
      ] },
      { ...missing, id: 11, evidenceKey: 'openlibrary', binderyEvidence: [
        { value: 'OL81608W', source: 'books.foreign_id', provider: 'openlibrary', foreignId: 'OL81608W' },
        { value: 'OL81608W', source: 'book_identifiers.foreign_id', provider: 'openlibrary', foreignId: 'OL81608W' },
      ] },
      { ...missing, id: 12, evidenceKey: 'isbn', assessment: 'ambiguous', binderyEvidence: [
        { value: '9781476713342', source: 'editions.isbn_13', provider: 'openlibrary', foreignId: 'OL4M' },
        { value: '9781476713342', source: 'calibre_identity_evidence.exact_editions', provider: 'openlibrary', foreignId: 'OL4M' },
        { value: '1476713340', source: 'editions.isbn_10', provider: 'openlibrary', foreignId: 'OL4M' },
        { value: '9780306406157', source: 'editions.isbn_13', provider: 'openlibrary', foreignId: 'OL5M' },
      ] },
    ], total: 3, limit: 50, offset: 0 })
    renderPage()
    await screen.findByText('3 findings match these filters')
    const cards = ['Hardcover book ID', 'OpenLibrary work ID', 'ISBN'].map(type =>
      screen.getByRole('heading', { level: 4, name: `Identifier comparison: ${type}` }).closest('li')!)
    expect(cards).toHaveLength(3)
    const found = (card: HTMLElement) => within(card).getByText('Bindery found').parentElement!
    expect(within(found(cards[0])).getAllByRole('link', { name: 'hc:night-shift ↗' })).toHaveLength(1)
    expect(within(found(cards[0])).getByText(/stored metadata record/)).toBeInTheDocument()
    expect(within(found(cards[0])).getByText(/metadata provider/)).toBeInTheDocument()
    expect(within(found(cards[1])).getAllByRole('link', { name: 'OL81608W ↗' })).toHaveLength(1)
    expect(within(found(cards[2])).getAllByRole('link', { name: '9781476713342 ↗' })).toHaveLength(1)
    expect(within(found(cards[2])).getAllByText(/ISBN-13/)).toHaveLength(2)
    expect(within(found(cards[2])).getByText(/ISBN-10/)).toBeInTheDocument()
    expect(within(found(cards[2])).getByRole('link', { name: '1476713340 ↗' })).toBeInTheDocument()
    expect(within(found(cards[2])).getByRole('link', { name: '9780306406157 ↗' })).toBeInTheDocument()
    expect(api.calibreAuditIdentifierAdd).not.toHaveBeenCalled()
    expect(api.calibreAuditIdentity).toHaveBeenCalledTimes(1)
    const response = await vi.mocked(api.calibreAudit).mock.results[0].value as { items: CalibreAuditFinding[] }
    expect(response.items[0].binderyEvidence.map(e => e.recordId)).toEqual([1, 2, undefined])
    expect(response.items[2].binderyEvidence).toHaveLength(4)
    expect(response.items[0].comparisonFingerprint).toBe('fp1')
  })

  it('names each identifier comparison on separate cards for the same book and keeps edition states independent', async () => {
    vi.mocked(api.getSetting).mockImplementation(async key => ({ key, value: key === 'calibre.identifier_write_enabled' ? 'true' : '' }))
    vi.mocked(api.calibreAudit).mockResolvedValue({ items: [
      { ...missing, id: 10, evidenceKey: 'openlibrary_edition', assessment: 'ambiguous',
        binderyEvidence: [{ value: 'OL4M', source: 'editions.foreign_id', provider: 'openlibrary', foreignId: 'OL4M' }] },
      { ...missing, id: 11, evidenceKey: 'isbn', findingType: 'identifier_conflict', assessment: 'ambiguous',
        calibreEvidence: [{ value: '9780306406157', source: 'calibre.identifiers.isbn' }],
        binderyEvidence: [{ value: '9780306406158', source: 'editions.isbn_13', provider: 'openlibrary' }] },
    ], total: 2, limit: 50, offset: 0 })
    vi.mocked(api.calibreAuditIdentity).mockResolvedValue({ bookId: 5, calibreId: 17, rootKey: 'openlibrary:OL1W', evidence: [],
      edition: { confidence: 'high', editionId: 'OL4M', provider: 'openlibrary', reasonCode: 'calibre_identifiers',
        reason: 'CWA ISBN matches this edition.', candidates: [{ editionId: 'OL4M', provider: 'openlibrary', reasons: [], claims: [{ type: 'isbn', value: '9780306406157' }] }] } })
    renderPage()
    expect(await screen.findAllByText(/This edition is very likely/)).toHaveLength(1)
    const cards = ['OpenLibrary edition ID', 'ISBN'].map(type =>
      screen.getByRole('heading', { level: 4, name: `Identifier comparison: ${type}` }).closest('li')!)
    expect(cards).toHaveLength(2)
    expect(cards[0]).toHaveTextContent('Identifier comparison: OpenLibrary edition ID')
    expect(cards[0]).toHaveTextContent('Identifier missing')
    expect(cards[0]).toHaveTextContent('Calibre has no OpenLibrary edition ID')
    expect(cards[1]).toHaveTextContent('Identifier comparison: ISBN')
    expect(cards[1]).toHaveTextContent('Identifier conflict')
    expect(cards[1]).toHaveTextContent('Calibre and Bindery have different ISBNs')
    expect(cards[1]).not.toHaveTextContent('Calibre and Bindery agree on this identifier')
    expect(screen.getByRole('heading', { level: 4, name: 'Edition match' })).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Review proposed identifier' })).not.toBeInTheDocument()
    expect(api.calibreAuditIdentifierProposals).not.toHaveBeenCalled()
  })

  it('uses the comparison state for normalized agreement, not the former missing explanation', async () => {
    vi.mocked(api.calibreAudit).mockResolvedValue({ items: [{ ...missing, state: 'resolved', evidenceKey: 'isbn', assessment: 'ambiguous',
      reason: "Current values agree under the field's conservative audit normalization.",
      calibreEvidence: [{ value: '9781476713342', source: 'calibre.identifiers.isbn' }],
      binderyEvidence: [{ value: '9781476713342', source: 'editions.isbn_13', provider: 'openlibrary' },
        { value: '1476713340', source: 'editions.isbn_10', provider: 'openlibrary' }] }], total: 1, limit: 50, offset: 0 })
    renderPage()
    const card = (await screen.findByText('Identifier comparison: ISBN')).closest('li')
    expect(card).toHaveTextContent('Identifier comparison: ISBN')
    expect(card).toHaveTextContent('Calibre and Bindery agree on this identifier')
    expect(card).not.toHaveTextContent('Calibre has no')
    expect(card).toHaveTextContent('1476713340')
    expect(card).toHaveTextContent('ISBN-10')
    expect(card).not.toHaveTextContent('Identifier missing')
    expect(screen.queryByRole('button', { name: 'Review proposed identifier' })).not.toBeInTheDocument()
  })

  it('attributes likely edition support to another Calibre identifier, not the missing audited type', async () => {
    vi.mocked(api.calibreAudit).mockResolvedValue({ items: [{ ...missing, evidenceKey: 'openlibrary_edition', assessment: 'ambiguous',
      binderyEvidence: [{ value: 'OL4M', source: 'calibre_identity_evidence.exact_editions', provider: 'openlibrary' }] }], total: 1, limit: 50, offset: 0 })
    vi.mocked(api.calibreAuditIdentity).mockResolvedValue({ bookId: 5, calibreId: 17, rootKey: 'openlibrary:OL1W', evidence: [],
      edition: { confidence: 'high', editionId: 'OL4M', provider: 'openlibrary', reasonCode: 'calibre_identifiers',
        reason: 'A CWA ISBN matches one provider edition.', candidates: [{ editionId: 'OL4M', provider: 'openlibrary', reasons: [], claims: [{ type: 'isbn', value: '9781476713342' }] }] } })
    renderPage()
    expect(await screen.findByText(/This edition is very likely/)).toBeInTheDocument()
    expect(screen.getByText(/Calibre stores an ISBN that points to this edition/)).toBeInTheDocument()
    expect(screen.getByRole('heading', { level: 4, name: 'Identifier comparison: OpenLibrary edition ID' }).closest('li')).toHaveTextContent('Calibre has no OpenLibrary edition ID')
    expect(screen.getByText(/Calibre stores an ISBN that points to this edition/).closest('section')).not.toHaveTextContent('Calibre has no OpenLibrary edition ID')
    expect(screen.queryByText(/An identifier stored in Calibre points to this edition/)).not.toBeInTheDocument()
    expect(screen.getByText(/not separate confirmation from data contained in the book file/)).toBeInTheDocument()
  })

  it('does not attribute stored metadata or provider evidence to Calibre when no Calibre reason was selected', async () => {
    vi.mocked(api.calibreAudit).mockResolvedValue({ items: [{ ...missing, evidenceKey: 'openlibrary_edition', assessment: 'ambiguous',
      binderyEvidence: [{ value: 'OL4M', source: 'editions.foreign_id', provider: 'openlibrary', foreignId: 'OL4M' },
        { value: 'OL4M', source: 'calibre_identity_evidence.exact_editions', provider: 'openlibrary', foreignId: 'OL4M' }] }], total: 1, limit: 50, offset: 0 })
    vi.mocked(api.calibreAuditIdentity).mockResolvedValue({ bookId: 5, calibreId: 17, rootKey: 'openlibrary:OL1W', evidence: [],
      edition: { confidence: 'high', editionId: 'OL4M', provider: 'openlibrary',
        reason: 'Older snapshot has no source category.', candidates: [{ editionId: 'OL4M', provider: 'openlibrary', reasons: [] }] } })
    renderPage()
    expect(await screen.findByText(/This edition is very likely/)).toBeInTheDocument()
    const card = screen.getByText('Identifier comparison: OpenLibrary edition ID').closest('li')!
    expect(card).toHaveTextContent('Calibre has no OpenLibrary edition ID')
    expect(card).toHaveTextContent('From a stored metadata record')
    expect(card).toHaveTextContent('Found via metadata provider')
    expect(screen.getByText(/The evidence points to this edition/)).toBeInTheDocument()
    expect(card).not.toHaveTextContent('An identifier stored in Calibre points to this edition')
  })

  it('does not request artwork before the paged identity snapshot or for historical rows', async () => {
    vi.mocked(api.calibreAudit).mockResolvedValue({ items: [{ ...finding, state: 'unmatched' }], total: 19000, limit: 50, offset: 0 })
    renderPage()
    expect(await screen.findByText('Previously stored in Calibre')).toBeInTheDocument()
    expect(screen.queryByRole('img')).not.toBeInTheDocument()
    expect(api.calibreAuditIdentity).not.toHaveBeenCalled()
  })
})
