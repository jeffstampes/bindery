import { beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor, fireEvent } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import { api, ApiError, type CalibreAuditFinding, type CalibreIdentifierProposal } from '../api/client'
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
  matchMethod: 'isbn', matchConfidence: 'exact', reason: 'Editions may differ.',
  comparisonFingerprint: 'fp1', state: 'unresolved', createdAt: '2026-01-01', updatedAt: '2026-01-01',
}

const missing: CalibreAuditFinding = { ...finding, field: 'identifiers', evidenceKey: 'openlibrary', findingType: 'identifier_missing', assessment: 'needs_review', calibreEvidence: [] }
const proposal: CalibreIdentifierProposal = {
  findingId: 3, bookId: 5, calibreId: 17, comparisonFingerprint: 'fp1', identifierType: 'openlibrary',
  currentValue: '', proposedValue: 'OL12W', action: 'add', evidenceKeys: ['openlibrary:OL12W'],
  evidence: [{ value: 'OL12W', source: 'books.foreign_id', provider: 'openlibrary', foreignId: 'OL12W' }],
  reason: 'Canonical provider work record',
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
  ], edition: { confidence: 'ambiguous', reason: 'Several editions share the claimed ISBN.', candidates: [
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
  it('runs reconciliation, shows its stage, disables both starts, and reports completion', async () => {
    vi.mocked(api.calibreReconcileStatus).mockResolvedValueOnce({ state: 'idle', completedStages: [] })
      .mockResolvedValueOnce({ state: 'completed', completedStages: ['ownership', 'identity', 'audit'], result: {
        totalBinderyBooks: 7, totalCalibreBooks: 90, matched: 1, revalidated: 4, stale: 1, unmatched: 1, artifactScansCached: 3,
        identity: { refreshedWorks: 2, evidenceRecords: 6, failedLookups: 0, truncatedLookups: 0, unconfiguredLookups: 0, notAttemptedLookups: 0, deferredWorks: 0, unresolvedRoots: 0, discoveryUnavailable: false },
        audit: { totalCalibreBooks: 90, comparedBooks: 5, findings: 3, updated: 1 },
        transitions: { newUnresolved: 1, resolved: 0, ignoredPreserved: 2, becameHistorical: 0 },
      } })
    renderPage()
    const run = await screen.findByRole('button', { name: 'Run reconciliation' })
    await waitFor(() => expect(run).toBeEnabled())
    fireEvent.click(run)
    expect(await screen.findByText(/Running: ownership matching/)).toBeInTheDocument()
    expect(run).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Recheck library' })).toBeDisabled()
    expect(await screen.findByText(/Ownership: 7 Bindery books/, {}, { timeout: 3500 })).toBeInTheDocument()
    expect(screen.getByText(/2 works refreshed, 6 provider evidence records/)).toBeInTheDocument()
    expect(screen.getByText(/3 cached file scans for current ownership links; no files rescanned/)).toBeInTheDocument()
    expect(screen.getByText(/1 newly unresolved, 0 resolved, 2 ignored decisions preserved/)).toBeInTheDocument()
    await waitFor(() => expect(api.calibreAudit).toHaveBeenCalledTimes(2))
  })

  it('shows partial provider outcomes as incomplete discovery, not clean success', async () => {
    vi.mocked(api.calibreReconcileStatus).mockResolvedValue({ state: 'partial', completedStages: ['ownership', 'identity', 'audit'], result: {
      totalBinderyBooks: 1, totalCalibreBooks: 5, matched: 0, revalidated: 1, stale: 0, unmatched: 0, artifactScansCached: 0,
      identity: { refreshedWorks: 1, evidenceRecords: 2, failedLookups: 1, truncatedLookups: 1, unconfiguredLookups: 1, notAttemptedLookups: 0, deferredWorks: 3, unresolvedRoots: 0, discoveryUnavailable: false },
    } })
    renderPage()
    expect(await screen.findByText(/Discovery was partial/)).toBeInTheDocument()
    expect(screen.getByText(/1 failed, 1 truncated, 1 unconfigured/)).toBeInTheDocument()
  })

  it('restores a failed run and surfaces start errors without a success summary', async () => {
    vi.mocked(api.calibreReconcileStatus).mockResolvedValue({ state: 'failed', completedStages: ['ownership'], error: 'read-only library unavailable' })
    vi.mocked(api.calibreReconcile).mockRejectedValueOnce(new Error('worker unavailable'))
    renderPage()
    expect(await screen.findByText(/Reconciliation failed: read-only library unavailable/)).toBeInTheDocument()
    const run = screen.getByRole('button', { name: 'Run reconciliation' })
    await waitFor(() => expect(run).toBeEnabled())
    fireEvent.click(run)
    expect(await screen.findByText(/Could not start reconciliation: worker unavailable/)).toBeInTheDocument()
    expect(screen.queryByText(/Reconciliation completed/)).not.toBeInTheDocument()
  })

  it('follows an existing reconciliation on conflict without starting a second job', async () => {
    vi.mocked(api.calibreReconcile).mockRejectedValueOnce(new ApiError(409, { state: 'running' }, 'busy'))
    vi.mocked(api.calibreReconcileStatus).mockResolvedValueOnce({ state: 'idle', completedStages: [] })
      .mockResolvedValueOnce({ state: 'running', stage: 'identity', completedStages: ['ownership'] })
    renderPage()
    const run = await screen.findByRole('button', { name: 'Run reconciliation' })
    await waitFor(() => expect(run).toBeEnabled())
    fireEvent.click(run)
    expect(await screen.findByText(/Running: identity evidence/)).toBeInTheDocument()
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
    expect(screen.queryByRole('button', { name: 'Preview identifier additions' })).not.toBeInTheDocument()
  })

  it('fails closed when the independent write setting is absent, false or unreadable', async () => {
    vi.mocked(api.calibreAudit).mockResolvedValue({ items: [missing], total: 1, limit: 50, offset: 0 })
    for (const value of ['false', 'yes', '']) {
      vi.mocked(api.getSetting).mockImplementation(async key => ({ key, value: key === 'calibre.identifier_write_enabled' ? value : '' }))
      const view = renderPage()
      await screen.findByText('No stored value')
      await waitFor(() => expect(api.getSetting).toHaveBeenCalledWith('calibre.identifier_write_enabled'))
      expect(screen.queryByRole('button', { name: 'Preview identifier additions' })).not.toBeInTheDocument()
      view.unmount()
    }
    vi.mocked(api.getSetting).mockRejectedValue(new Error('setting unavailable'))
    const view = renderPage()
    await screen.findByText('No stored value')
    expect(screen.queryByRole('button', { name: 'Preview identifier additions' })).not.toBeInTheDocument()
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
    fireEvent.click(await screen.findByRole('button', { name: 'Preview identifier additions' }))
    expect(await screen.findByText(/Canonical provider work record/)).toBeInTheDocument()
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
    fireEvent.click(await screen.findByRole('button', { name: 'Preview identifier additions' }))
    expect((await screen.findAllByRole('link', { name: 'known-slug ↗' })).every(link => link.getAttribute('href') === 'https://hardcover.app/books/known-slug')).toBe(true)
    expect(screen.getAllByRole('link', { name: 'known-slug ↗' })[0]).toHaveAttribute('rel', 'noopener noreferrer')
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
    expect(screen.queryByRole('button', { name: 'Preview identifier additions' })).not.toBeInTheDocument()
  })

  it('rejects stale proposals without posting and refreshes after a stale apply', async () => {
    vi.mocked(api.getSetting).mockImplementation(async key => ({ key, value: key === 'calibre.identifier_write_enabled' ? 'true' : '' }))
    vi.mocked(api.calibreAudit).mockResolvedValue({ items: [missing], total: 1, limit: 50, offset: 0 })
    vi.mocked(api.calibreAuditIdentifierProposals).mockResolvedValueOnce({ items: [{ ...proposal, comparisonFingerprint: 'older' }] }).mockResolvedValueOnce({ items: [proposal] })
    vi.mocked(api.calibreAuditIdentifierAdd).mockRejectedValueOnce(new ApiError(409, { error: 'stale' }, 'stale'))
    renderPage()
    const preview = await screen.findByRole('button', { name: 'Preview identifier additions' })
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
    fireEvent.click(await screen.findByRole('button', { name: 'Preview identifier additions' }))
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
    const preview = await screen.findByRole('button', { name: 'Preview identifier additions' })
    fireEvent.click(preview)
    expect(await screen.findByText(/No eligible missing work-level identifier additions/)).toBeInTheDocument()
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
    expect(screen.getByText(/Editions may differ/)).toBeInTheDocument()
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
    expect(screen.getByText('Previously recorded Calibre/CWA value')).toBeInTheDocument()
    expect(screen.queryByText(/Why this was flagged/)).not.toBeInTheDocument()
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
    vi.mocked(api.calibreAudit).mockResolvedValue({ items: [{ ...finding, field: 'identifiers', evidenceKey: 'asin',
      findingType: 'identifier_missing', assessment: 'ambiguous' }], total: 1, limit: 50, offset: 0 })
    renderPage()
    expect(await screen.findByText('Edition-oriented identifier evidence (not an edition match)')).toBeInTheDocument()
    expect(screen.getByText(/no exact owned edition/)).toBeInTheDocument()
    expect(api.calibreAuditIdentity).not.toHaveBeenCalled()
    fireEvent.click(screen.getByRole('button', { name: 'Show work and edition evidence' }))
    expect(await screen.findByText(/Provider work evidence: canonical. Edition evidence: ambiguous/)).toBeInTheDocument()
    expect(screen.getByText('Several editions share the claimed ISBN.')).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'OL4M ↗' }).closest('li')).toHaveTextContent('openlibrary: OL4M ↗')
    expect(screen.getByRole('link', { name: 'OL5M ↗' }).closest('li')).toHaveTextContent('openlibrary: OL5M ↗')
    expect(api.calibreAuditIdentity).toHaveBeenCalledWith(5)
  })

  it('links typed CWA claims separately from edition-candidate explanations without changing confidence or actions', async () => {
    vi.mocked(api.getSetting).mockImplementation(async key => ({ key, value: key === 'calibre.identifier_write_enabled' ? 'true' : '' }))
    vi.mocked(api.calibreAuditIdentity).mockResolvedValue({ bookId: 5, calibreId: 17, rootKey: 'openlibrary:OL1W', evidence: [
      { status: 'root', workConfidence: 'canonical', editionConfidence: 'unresolved' },
    ], edition: { confidence: 'ambiguous', reason: 'Several editions share the claimed ISBN.', candidates: [
      { provider: 'openlibrary', editionId: 'OL4M', reasons: ['CWA claims (one correlated source)', 'pre-write-back file ISBN matches this edition'], claims: [
        { type: 'openlibrary_edition', value: 'OL4M' }, { type: 'isbn', value: '9780306406157' },
      ] },
    ] } })
    renderPage()
    fireEvent.click(await screen.findByRole('button', { name: 'Show work and edition evidence' }))
    const isbn = await screen.findByRole('link', { name: '9780306406157 ↗' })
    expect(isbn).toHaveAttribute('href', 'https://openlibrary.org/isbn/9780306406157')
    expect(isbn).toHaveAttribute('target', '_blank')
    expect(isbn).toHaveAttribute('rel', 'noopener noreferrer')
    expect(screen.getAllByRole('link', { name: 'OL4M ↗' })).toHaveLength(2)
    expect(isbn.closest('li')).toHaveTextContent('CWA claims (one correlated source); pre-write-back file ISBN matches this edition · openlibrary_edition:OL4M ↗, isbn:9780306406157 ↗')
    expect(screen.getByText(/Edition evidence: ambiguous/)).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Preview identifier additions' })).not.toBeInTheDocument()
  })

  it('shows the selected owned edition without calling a candidate confirmed by evidence status alone', async () => {
    vi.mocked(api.calibreAuditIdentity).mockResolvedValue({ bookId: 5, calibreId: 17, rootKey: 'openlibrary:OL1W', evidence: [
      { status: 'root', workConfidence: 'canonical', editionConfidence: 'unresolved' },
      { status: 'root', workConfidence: 'exact', editionConfidence: 'unresolved', editionId: 'OL4M' },
    ], edition: { confidence: 'high', editionId: 'OL4M', provider: 'openlibrary',
      reason: 'A unique owned-book ISBN matches the edition.', candidates: [{ provider: 'openlibrary', editionId: 'OL4M', reasons: [] }] } })
    renderPage()
    fireEvent.click(await screen.findByRole('button', { name: 'Show work and edition evidence' }))
    expect((await screen.findAllByRole('link', { name: 'OL4M ↗' }))[0].closest('p')).toHaveTextContent('Edition evidence: high (OL4M ↗)')
    expect(screen.getByText('A unique owned-book ISBN matches the edition.')).toBeInTheDocument()
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
    expect(screen.getByText('9780306406158')).toBeInTheDocument()
    expect(screen.queryByRole('link', { name: /12345|9780306406158/ })).not.toBeInTheDocument()
    expect(screen.getByText('Current comparison · ambiguous')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Preview identifier additions' })).not.toBeInTheDocument()
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
    expect(await screen.findByText('Work/provider identifier')).toBeInTheDocument()
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
})
