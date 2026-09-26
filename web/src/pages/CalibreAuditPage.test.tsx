import { beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor, fireEvent } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import { api, ApiError, type CalibreAuditFinding } from '../api/client'
import CalibreAuditPage from './CalibreAuditPage'
import en from '../i18n/locales/en.json'

vi.mock('../api/client', async importOriginal => {
  const original = await importOriginal<typeof import('../api/client')>()
  return { ...original, api: { ...original.api,
    calibreAudit: vi.fn(), calibreAuditIgnore: vi.fn(), calibreAuditRecheck: vi.fn(), calibreAuditRecheckStatus: vi.fn(), getSetting: vi.fn(),
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

function renderPage() { return render(<MemoryRouter><CalibreAuditPage /></MemoryRouter>) }

beforeEach(() => {
  vi.clearAllMocks()
  localStorage.clear()
  vi.mocked(api.getSetting).mockResolvedValue({ key: 'cwa.web_url', value: '' })
  vi.mocked(api.calibreAudit).mockResolvedValue({ items: [finding], total: 1, limit: 50, offset: 0 })
  vi.mocked(api.calibreAuditIgnore).mockResolvedValue(undefined)
  vi.mocked(api.calibreAuditRecheck).mockResolvedValue({ running: true, startedAt: '2026-01-01' })
  vi.mocked(api.calibreAuditRecheckStatus).mockResolvedValue({ running: false })
})

describe('CalibreAuditPage', () => {
  it('shows owned and provider evidence separately with source, reason, and ambiguity', async () => {
    renderPage()
    expect(await screen.findByText('Owned title')).toBeInTheDocument()
    expect(screen.getAllByText('Provider title')).toHaveLength(2)
    expect(screen.getByText(/openlibrary · OL1W/)).toBeInTheDocument()
    expect(screen.getByText(/Editions may differ/)).toBeInTheDocument()
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
    expect(screen.queryByRole('button', { name: 'Ignore this comparison' })).not.toBeInTheDocument()
    expect(await screen.findByRole('link', { name: /Open in CWA/ })).toHaveAttribute('href', 'https://cwa.example.org/root/book/17')
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
