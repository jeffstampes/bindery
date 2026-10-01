import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import { api } from '../../api/client'
import CalibreTab from './CalibreTab'

vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (_: string, fallback?: unknown) => typeof fallback === 'string' ? fallback : _, i18n: { changeLanguage: vi.fn() } }),
}))
vi.mock('../../api/client', async importOriginal => {
  const actual = await importOriginal<typeof import('../../api/client')>()
  return { ...actual, api: {
    ...actual.api,
    listSettings: vi.fn(), setSetting: vi.fn(),
    calibreImportStatus: vi.fn(), calibreSyncStatus: vi.fn(), calibreRuns: vi.fn(),
  } }
})

function seedSettings(values: Record<string, string> = {}) {
  vi.mocked(api.listSettings).mockResolvedValue(
    Object.entries(values).map(([key, value]) => ({ key, value })) as Awaited<ReturnType<typeof api.listSettings>>,
  )
}

beforeEach(() => {
  seedSettings()
  vi.mocked(api.setSetting).mockReset()
  vi.mocked(api.setSetting).mockResolvedValue(undefined as never)
  vi.mocked(api.calibreImportStatus).mockRejectedValue(new Error('no import'))
  vi.mocked(api.calibreSyncStatus).mockRejectedValue(new Error('no sync'))
  vi.mocked(api.calibreRuns).mockResolvedValue([])
})

async function refreshToggle() {
  return screen.findByRole('switch', { name: /metadata refresh/ })
}

describe('Calibre approved metadata refresh setting', () => {
  it('defaults off and is unavailable without authoritative mode', async () => {
    render(<MemoryRouter><CalibreTab /></MemoryRouter>)
    const toggle = await refreshToggle()
    expect(toggle).toHaveAttribute('aria-checked', 'false')
    expect(toggle).toBeDisabled()
    expect(screen.getByText(/Enable authoritative-library mode first/)).toBeInTheDocument()
    fireEvent.click(toggle)
    expect(api.setSetting).not.toHaveBeenCalled()
  })

  it('enables and disables independently of identifier writes', async () => {
    seedSettings({ 'calibre.authoritative_library_enabled': 'true', 'calibre.identifier_write_enabled': 'true' })
    render(<MemoryRouter><CalibreTab /></MemoryRouter>)
    const toggle = await refreshToggle()
    expect(toggle).toHaveAttribute('aria-checked', 'false')
    expect(toggle).toBeEnabled()
    fireEvent.click(toggle)
    await waitFor(() => expect(toggle).toHaveAttribute('aria-checked', 'true'))
    expect(api.setSetting).toHaveBeenCalledWith('calibre.metadata_refresh_enabled', 'true')
    fireEvent.click(toggle)
    await waitFor(() => expect(toggle).toHaveAttribute('aria-checked', 'false'))
    expect(api.setSetting).toHaveBeenCalledWith('calibre.metadata_refresh_enabled', 'false')
    expect(api.setSetting).not.toHaveBeenCalledWith('calibre.identifier_write_enabled', expect.anything())
  })

  it('retains the stored choice while authoritative mode is off', async () => {
    seedSettings({ 'calibre.metadata_refresh_enabled': 'true' })
    render(<MemoryRouter><CalibreTab /></MemoryRouter>)
    const toggle = await refreshToggle()
    expect(toggle).toHaveAttribute('aria-checked', 'true')
    expect(toggle).toBeDisabled()
    expect(api.setSetting).not.toHaveBeenCalled()
  })

  it('surfaces save failure without falsely showing success', async () => {
    seedSettings({ 'calibre.authoritative_library_enabled': 'true' })
    vi.mocked(api.setSetting).mockRejectedValue(new Error('library is read-only'))
    render(<MemoryRouter><CalibreTab /></MemoryRouter>)
    const toggle = await refreshToggle()
    fireEvent.click(toggle)
    expect(await screen.findByRole('alert')).toHaveTextContent('library is read-only')
    expect(toggle).toHaveAttribute('aria-checked', 'false')
  })
})
