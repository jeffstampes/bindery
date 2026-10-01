import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { api, type CalibreMetadataRefreshAttempt, type CalibreMetadataRefreshProposal } from '../api/client'

// One refresh per matched book, not one per audit finding. The server rechecks
// identity and writes only its frozen proposal; the browser sends no fields.
export default function CalibreMetadataRefresh({ bookId, calibreId, enabled, onApplied, onNotice }: {
  bookId: number
  calibreId: number
  enabled: boolean
  onApplied: () => void
  onNotice: (message: string, failed: boolean) => void
}) {
  const { t } = useTranslation()
  const [open, setOpen] = useState(false)
  const [proposal, setProposal] = useState<CalibreMetadataRefreshProposal | null>(null)
  const [attempts, setAttempts] = useState<CalibreMetadataRefreshAttempt[]>([])
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [revision, setRevision] = useState(0)
  useEffect(() => {
    if (!open || !enabled) return
    let active = true
    api.calibreMetadataRefreshAttempts(bookId).then(data => { if (active) setAttempts(data.items) })
      .catch(err => { if (active) setError(t('calibreAudit.refresh.error', { error: err instanceof Error ? err.message : String(err) })) })
    return () => { active = false }
  }, [open, enabled, bookId, revision, t])

  const preview = async () => {
    setBusy(true); setError(''); setProposal(null)
    try {
      const next = await api.calibreMetadataRefreshPreview(bookId)
      if (next.bookId !== bookId || next.calibreId !== calibreId) throw new Error('The matched Calibre book changed; reload the review.')
      setProposal(next)
    } catch (err) {
      setError(t('calibreAudit.refresh.error', { error: err instanceof Error ? err.message : String(err) }))
    } finally { setBusy(false) }
  }
  const apply = async () => {
    if (!proposal || proposal.status !== 'ready' || !proposal.fields.some(f => f.status === 'change')) return
    setBusy(true); setError('')
    try {
      const result = await api.calibreMetadataRefreshApply(proposal.id, proposal.fingerprint)
      setProposal(null)
      setRevision(n => n + 1)
      if (result.outcome === 'applied') {
        onNotice(t('calibreAudit.refresh.success'), false)
        onApplied()
      } else onNotice(t('calibreAudit.refresh.failure', { error: result.error || result.outcome }), true)
    } catch (err) {
      setProposal(null) // partial state must never leave an actionable approval
      setRevision(n => n + 1)
      onNotice(t('calibreAudit.refresh.failure', { error: err instanceof Error ? err.message : String(err) }), true)
    } finally { setBusy(false) }
  }
  return <section className="rounded border border-slate-300 dark:border-zinc-700 p-3 space-y-2 text-sm">
    <button type="button" className="font-medium text-emerald-700 dark:text-emerald-400 underline" onClick={() => setOpen(!open)} aria-expanded={open}>{t('calibreAudit.refresh.title')}</button>
    {open && <div className="space-y-2">
      <p className="text-fg-muted">{t('calibreAudit.refresh.scope')}</p>
      {!enabled ? <p>{t('calibreAudit.refresh.disabled')}</p> : <>
        <button type="button" onClick={preview} disabled={busy} className="rounded border px-2 py-1 disabled:opacity-50">{busy ? t('calibreAudit.refresh.previewing') : t('calibreAudit.refresh.preview')}</button>
        {error && <p role="alert" className="text-red-600 dark:text-red-400">{error}</p>}
        {proposal && <div className="space-y-2">
          <p>{t('calibreAudit.refresh.identity', { work: proposal.rootKey, calibre: proposal.calibreId, edition: proposal.edition?.editionId || t('calibreAudit.unknown'), confidence: proposal.edition?.confidence || t('calibreAudit.unknown') })}</p>
          <p className="text-xs text-fg-muted">{t('calibreAudit.refresh.lookup', { isbn: proposal.lookupIsbn || t('calibreAudit.unknown') })} · {proposal.evidenceKey}</p>
          {proposal.fetchedOpfDigest && <p className="text-xs text-fg-muted">{t('calibreAudit.refresh.digest', { digest: proposal.fetchedOpfDigest })}</p>}
          <p className="text-xs text-fg-muted">{t('calibreAudit.refresh.sourceDigest', { digest: proposal.sourceDigest })}</p>
          {proposal.lookupLog && <p className="text-xs text-fg-muted break-words">{t('calibreAudit.refresh.providerLog', { log: proposal.lookupLog })}</p>}
          <p role="status">{t('calibreAudit.refresh.status', { status: proposal.status, reason: proposal.reason || '' })}</p>
          <h4 className="font-medium">{t('calibreAudit.refresh.identifiers')}</h4>
          <ul>{Object.entries(proposal.currentIdentifiers).sort(([a], [b]) => a.localeCompare(b)).map(([type, value]) =>
            <li key={type} className="break-words">{t('calibreAudit.refresh.preserved', { type, value })}</li>)}</ul>
          <h4 className="font-medium">{t('calibreAudit.refresh.proposedIdentifiers')}</h4>
          <ul>{Object.entries(proposal.proposedIdentifiers).sort(([a], [b]) => a.localeCompare(b)).map(([type, value]) =>
            <li key={type} className="break-words">{type}: {value}</li>)}</ul>
          <p>{t('calibreAudit.refresh.destructiveIdentifiers')}</p>
          <h4 className="font-medium">{t('calibreAudit.refresh.fields')}</h4>
          <ul className="space-y-1">{proposal.fields.map(field => <li key={field.name} className="break-words">
            {t('calibreAudit.refresh.field', { name: field.name, current: field.current || t('calibreAudit.refresh.missingValue'), fetched: field.fetched || t('calibreAudit.refresh.missingValue'), status: field.status })}
            {field.reason && <span className="block text-xs text-fg-muted">{t('calibreAudit.refresh.reason', { reason: field.reason })}</span>}
          </li>)}</ul>
          {proposal.status === 'ready' && proposal.fields.some(field => field.status === 'change') &&
            <button type="button" onClick={apply} disabled={busy} className="rounded bg-emerald-700 text-white px-2 py-1 disabled:opacity-50">{busy ? t('calibreAudit.refresh.working') : t('calibreAudit.refresh.approve')}</button>}
        </div>}
        <details><summary className="cursor-pointer">{t('calibreAudit.refresh.history')}</summary>
          {attempts.length === 0 ? <p>{t('calibreAudit.refresh.none')}</p> : <ul>{attempts.map(attempt =>
            <li key={attempt.id}>{t('calibreAudit.refresh.attempt', { id: attempt.id, proposal: attempt.proposalId, outcome: attempt.outcome, detail: attempt.error || attempt.verification || '' })}</li>)}</ul>}
        </details>
      </>}
    </div>}
  </section>
}
