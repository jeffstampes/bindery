import { beforeEach, expect, it, vi } from 'vitest'
import { render, screen, waitFor, fireEvent } from '@testing-library/react'
import { api, ApiError, type CalibreMetadataRefreshProposal } from '../api/client'
import CalibreMetadataRefresh from './CalibreMetadataRefresh'
import en from '../i18n/locales/en.json'

vi.mock('../api/client', async importOriginal => {
  const original = await importOriginal<typeof import('../api/client')>()
  return { ...original, api: { ...original.api,
    calibreMetadataRefreshEligibility: vi.fn(), calibreMetadataRefreshPreview: vi.fn(), calibreMetadataRefreshApply: vi.fn(), calibreMetadataRefreshAttempts: vi.fn(),
  } }
})
vi.mock('react-i18next', () => {
  const t = (key: string, opts?: Record<string, unknown>) => {
    const value = key.split('.').reduce<unknown>((node, part) => (node as Record<string, unknown>)?.[part], en)
    return typeof value === 'string' ? value.replace(/{{(\w+)}}/g, (_, part: string) => String(opts?.[part] ?? '')) : key
  }
  return { useTranslation: () => ({ t }) }
})
const proposal: CalibreMetadataRefreshProposal = {
  id: 10, bookId: 5, calibreId: 17, rootKey: 'openlibrary:OL100W', evidenceKey: 'root',
  edition: { confidence: 'exact', editionId: 'OL40M', reason: '', reasonCode: 'independent_file_isbn', candidates: [] },
  lookupIsbn: '9780306406157', currentIdentifiers: { isbn: '9780306406157', unknown: 'retain-me' },
  proposedIdentifiers: { isbn: '9780306406157', unknown: 'retain-me' },
  fields: [
    { name: 'title', current: 'Owned title', fetched: 'Provider Work Title', status: 'change' },
    { name: 'publisher', current: 'Existing press', fetched: 'New press', status: 'change' },
    { name: 'authors', current: 'Curated author', fetched: 'Provider author', status: 'withheld', reason: 'Not writable.' },
    { name: 'pubdate', current: '2020', fetched: '', status: 'missing', reason: 'Omitted.' },
  ], fetchedOpfDigest: 'digest', sourceDigest: 'source-hash', lookupLog: 'Google answered', status: 'ready', fingerprint: 'frozen', version: 1,
}

beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(api.calibreMetadataRefreshEligibility).mockResolvedValue({ status: 'eligible', confidence: 'exact', matchMethod: 'identifier:isbn' })
  vi.mocked(api.calibreMetadataRefreshPreview).mockResolvedValue(proposal)
  vi.mocked(api.calibreMetadataRefreshAttempts).mockResolvedValue({ items: [] })
  vi.mocked(api.calibreMetadataRefreshApply).mockResolvedValue({ id: 1, proposalId: 10, actorUserId: 7, bookId: 5, calibreId: 17, action: 'apply_fields', outcome: 'applied', startedAt: '2026-01-01' })
})

it('shows book-level frozen preview, retained identifiers, withheld and missing fields before approval', async () => {
  const onApplied = vi.fn(), onNotice = vi.fn()
  render(<CalibreMetadataRefresh bookId={5} calibreId={17} enabled onApplied={onApplied} onNotice={onNotice} />)
  expect(api.calibreMetadataRefreshPreview).not.toHaveBeenCalled()
  fireEvent.click(screen.getByRole('button', { name: en.calibreAudit.refresh.title }))
  expect(screen.queryByRole('button', { name: en.calibreAudit.refresh.approve })).not.toBeInTheDocument()
  await waitFor(() => expect(screen.getByRole('button', { name: en.calibreAudit.refresh.preview })).toBeEnabled())
  fireEvent.click(screen.getByRole('button', { name: en.calibreAudit.refresh.preview }))
  await waitFor(() => expect(screen.getAllByText(/unknown: retain-me/)).toHaveLength(2))
  expect(screen.getByText(/title: Owned title → Provider Work Title \(change\)/)).toBeInTheDocument()
  expect(screen.getByText(/authors: Curated author → Provider author \(withheld\)/)).toBeInTheDocument()
  expect(screen.getByText(/pubdate: 2020 → not supplied \(missing\)/)).toBeInTheDocument()
  expect(api.calibreMetadataRefreshApply).not.toHaveBeenCalled()
  fireEvent.click(screen.getByRole('button', { name: en.calibreAudit.refresh.approve }))
  await waitFor(() => expect(api.calibreMetadataRefreshApply).toHaveBeenCalledWith(10, 'frozen'))
  expect(onNotice).toHaveBeenCalledWith(en.calibreAudit.refresh.success, false)
  expect(onApplied).toHaveBeenCalledOnce()
})

it('shows the backend reason and disables preview for medium ownership without fetching', async () => {
  vi.mocked(api.calibreMetadataRefreshEligibility).mockResolvedValue({
    status: 'ineligible', confidence: 'medium', matchMethod: 'fallback_title_author',
    reason: 'Metadata refresh requires an identifier-confirmed ownership match. This book is currently matched by title and author.',
  })
  render(<CalibreMetadataRefresh bookId={5} calibreId={17} enabled onApplied={vi.fn()} onNotice={vi.fn()} />)
  fireEvent.click(screen.getByRole('button', { name: en.calibreAudit.refresh.title }))
  await waitFor(() => expect(screen.getByText(/currently matched by title and author/)).toBeInTheDocument())
  expect(screen.getByRole('button', { name: en.calibreAudit.refresh.preview })).toBeDisabled()
  expect(api.calibreMetadataRefreshPreview).not.toHaveBeenCalled()
  expect(api.calibreMetadataRefreshApply).not.toHaveBeenCalled()
})

it('does not offer approval if the server rejects a preview after an eligible hint', async () => {
  vi.mocked(api.calibreMetadataRefreshPreview).mockRejectedValueOnce(new ApiError(409,
    { code: 'ownership_ineligible', error: 'This book is currently matched by title and author.' }, 'Conflict'))
  render(<CalibreMetadataRefresh bookId={5} calibreId={17} enabled onApplied={vi.fn()} onNotice={vi.fn()} />)
  fireEvent.click(screen.getByRole('button', { name: en.calibreAudit.refresh.title }))
  await waitFor(() => expect(screen.getByRole('button', { name: en.calibreAudit.refresh.preview })).toBeEnabled())
  fireEvent.click(screen.getByRole('button', { name: en.calibreAudit.refresh.preview }))
  await waitFor(() => expect(screen.getByText(/currently matched by title and author/)).toBeInTheDocument())
  expect(screen.getByRole('button', { name: en.calibreAudit.refresh.preview })).toBeDisabled()
  expect(screen.queryByRole('button', { name: en.calibreAudit.refresh.approve })).not.toBeInTheDocument()
  expect(api.calibreMetadataRefreshApply).not.toHaveBeenCalled()
})

it('shows a real stale preview as stale rather than ownership-ineligible', async () => {
  vi.mocked(api.calibreMetadataRefreshPreview).mockRejectedValueOnce(new ApiError(409,
    { code: 'stale', error: 'metadata refresh proposal is stale; preview again' }, 'Conflict'))
  render(<CalibreMetadataRefresh bookId={5} calibreId={17} enabled onApplied={vi.fn()} onNotice={vi.fn()} />)
  fireEvent.click(screen.getByRole('button', { name: en.calibreAudit.refresh.title }))
  await waitFor(() => expect(screen.getByRole('button', { name: en.calibreAudit.refresh.preview })).toBeEnabled())
  fireEvent.click(screen.getByRole('button', { name: en.calibreAudit.refresh.preview }))
  await waitFor(() => expect(screen.getByText(/proposal is stale; preview again/)).toBeInTheDocument())
  expect(screen.getByRole('button', { name: en.calibreAudit.refresh.preview })).toBeDisabled()
  expect(api.calibreMetadataRefreshApply).not.toHaveBeenCalled()
})

it('fails closed when the eligibility request fails', async () => {
  vi.mocked(api.calibreMetadataRefreshEligibility).mockRejectedValueOnce(new Error('unavailable'))
  render(<CalibreMetadataRefresh bookId={5} calibreId={17} enabled onApplied={vi.fn()} onNotice={vi.fn()} />)
  fireEvent.click(screen.getByRole('button', { name: en.calibreAudit.refresh.title }))
  await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent('unavailable'))
  expect(screen.getByRole('button', { name: en.calibreAudit.refresh.preview })).toBeDisabled()
  expect(api.calibreMetadataRefreshPreview).not.toHaveBeenCalled()
})

it('does not allow apply for no-result, wrong match, or a failed/partial attempt', async () => {
  const onNotice = vi.fn()
  vi.mocked(api.calibreMetadataRefreshPreview).mockResolvedValueOnce({ ...proposal, status: 'no_result', fields: [], reason: 'No result' })
    .mockResolvedValueOnce({ ...proposal, calibreId: 99 })
    .mockResolvedValueOnce(proposal)
  vi.mocked(api.calibreMetadataRefreshApply).mockRejectedValueOnce(new Error('partial command failure'))
  render(<CalibreMetadataRefresh bookId={5} calibreId={17} enabled onApplied={vi.fn()} onNotice={onNotice} />)
  fireEvent.click(screen.getByRole('button', { name: en.calibreAudit.refresh.title }))
  await waitFor(() => expect(screen.getByRole('button', { name: en.calibreAudit.refresh.preview })).toBeEnabled())
  fireEvent.click(screen.getByRole('button', { name: en.calibreAudit.refresh.preview }))
  await waitFor(() => expect(screen.getByText(/No result/)).toBeInTheDocument())
  expect(screen.queryByRole('button', { name: en.calibreAudit.refresh.approve })).not.toBeInTheDocument()
  await waitFor(() => expect(screen.getByRole('button', { name: en.calibreAudit.refresh.preview })).toBeEnabled())
  fireEvent.click(screen.getByRole('button', { name: en.calibreAudit.refresh.preview }))
  await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent(/matched Calibre book changed/))
  fireEvent.click(screen.getByRole('button', { name: en.calibreAudit.refresh.preview }))
  await waitFor(() => expect(screen.getByRole('button', { name: en.calibreAudit.refresh.approve })).toBeInTheDocument())
  fireEvent.click(screen.getByRole('button', { name: en.calibreAudit.refresh.approve }))
  await waitFor(() => expect(onNotice).toHaveBeenCalledWith(expect.stringContaining('partial command failure'), true))
  expect(screen.queryByRole('button', { name: en.calibreAudit.refresh.approve })).not.toBeInTheDocument()
})
