import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, waitFor, fireEvent } from '@testing-library/react'
import CalibreTab from './CalibreTab'
import { api } from '../../api/client'

// Issue #2: the Calibre/CWA authoritative-library toggle is opt-in. It reads
// as off until the setting says otherwise, it saves through the generic
// settings endpoint, and a refusal from the backend (the mode needs a Calibre
// library path) leaves the switch where it was instead of pretending the save
// landed.

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string, fallback?: unknown) => (typeof fallback === 'string' ? fallback : key),
    i18n: { changeLanguage: vi.fn() },
  }),
}))

vi.mock('../../api/client', async importOriginal => {
  const actual = await importOriginal<typeof import('../../api/client')>()
  return {
    ...actual,
    api: {
      ...actual.api,
      listSettings: vi.fn(),
      setSetting: vi.fn(),
      testCalibre: vi.fn(),
      calibreImportStatus: vi.fn(),
      calibreSyncStatus: vi.fn(),
      calibreRuns: vi.fn(),
    },
  }
})

function seedSettings(entries: Record<string, string>) {
  vi.mocked(api.listSettings).mockResolvedValue(
    Object.entries(entries).map(([key, value]) => ({ key, value })) as Awaited<
      ReturnType<typeof api.listSettings>
    >,
  )
}

function findToggle() {
  return screen.findByTitle('Enable authoritative-library mode')
}

beforeEach(() => {
  seedSettings({})
  vi.mocked(api.setSetting).mockReset()
  vi.mocked(api.setSetting).mockResolvedValue(undefined as never)
  vi.mocked(api.testCalibre).mockRejectedValue(new Error('no calibre'))
  vi.mocked(api.calibreImportStatus).mockRejectedValue(new Error('no import'))
  vi.mocked(api.calibreSyncStatus).mockRejectedValue(new Error('no sync'))
  vi.mocked(api.calibreRuns).mockResolvedValue([])
})

describe('Calibre authoritative-library toggle', () => {
  it('is off when nothing is stored', async () => {
    render(<CalibreTab />)
    const toggle = await findToggle()
    expect(toggle).toHaveAttribute('aria-checked', 'false')
    expect(api.setSetting).not.toHaveBeenCalled()
  })

  it('reflects a stored true', async () => {
    seedSettings({ 'calibre.authoritative_library_enabled': 'true' })
    render(<CalibreTab />)
    const toggle = await screen.findByTitle('Disable authoritative-library mode')
    expect(toggle).toHaveAttribute('aria-checked', 'true')
  })

  it('saves true through the settings endpoint when switched on', async () => {
    seedSettings({ 'calibre.library_path': '/data/calibre' })
    render(<CalibreTab />)
    const toggle = await findToggle()

    fireEvent.click(toggle)

    await waitFor(() =>
      expect(api.setSetting).toHaveBeenCalledWith('calibre.authoritative_library_enabled', 'true'),
    )
    expect(await screen.findByTitle('Disable authoritative-library mode')).toHaveAttribute(
      'aria-checked',
      'true',
    )
  })

  it('saves false when switched back off', async () => {
    seedSettings({
      'calibre.library_path': '/data/calibre',
      'calibre.authoritative_library_enabled': 'true',
    })
    render(<CalibreTab />)
    const toggle = await screen.findByTitle('Disable authoritative-library mode')

    fireEvent.click(toggle)

    await waitFor(() =>
      expect(api.setSetting).toHaveBeenCalledWith('calibre.authoritative_library_enabled', 'false'),
    )
  })

  it('stays off and shows the reason when the backend refuses', async () => {
    vi.mocked(api.setSetting).mockRejectedValue(
      new Error('calibre.authoritative_library_enabled requires calibre.library_path'),
    )
    render(<CalibreTab />)
    const toggle = await findToggle()

    fireEvent.click(toggle)

    expect(
      await screen.findByText(/requires calibre\.library_path/),
    ).toBeInTheDocument()
    expect(await findToggle()).toHaveAttribute('aria-checked', 'false')
  })

  it('says the mode does not change the catalogue yet', async () => {
    render(<CalibreTab />)
    expect(
      await screen.findByText(/nothing changes in the catalogue yet/),
    ).toBeInTheDocument()
  })
})
