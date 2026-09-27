import { useCallback, useEffect, useState } from 'react'
import { Link } from 'react-router'
import { useTranslation } from 'react-i18next'
import { api, ApiError, type CalibreAuditEvidence, type CalibreAuditFinding, type CalibreAuditRecheckStatus, type CalibreIdentitySnapshot } from '../api/client'
import { metadataSourceLink, providerDisplayName } from '../util/metadataSource'
import Pagination from '../components/Pagination'
import { useServerPagination } from '../components/usePagination'

const TYPES = [
  'identifier_missing', 'identifier_conflict', 'title_difference', 'author_difference',
  'series_membership_difference', 'series_position_difference', 'language_difference', 'publication_date_difference',
]
const STATES = ['unresolved', 'unmatched', 'ignored', 'resolved']

// A filesystem/ingest path or plugin URL is not a CWA browser address. Only an
// explicitly configured, absolute web URL can form a reliable book detail link.
function cwaBookURL(base: string, id: number): string | null {
  if (!Number.isSafeInteger(id) || id <= 0) return null
  try {
    const url = new URL(base)
    if (!['http:', 'https:'].includes(url.protocol) || url.username || url.password || url.search || url.hash) return null
    return `${url.origin}${url.pathname.replace(/\/+$/, '')}/book/${id}`
  } catch { return null }
}

const EDITION_IDS = new Set(['isbn', 'asin', 'openlibrary_edition'])

function providerRecordLink(e: CalibreAuditEvidence) {
  const calibreID = e.source.startsWith('calibre.identifiers.')
  const provider = calibreID ? e.source.slice('calibre.identifiers.'.length).toLowerCase() : e.provider?.toLowerCase()
  const rawID = (calibreID ? e.value : e.foreignId)?.trim() ?? ''
  const id = provider === 'google' || provider === 'googlebooks'
    ? (rawID.startsWith('gb:') ? rawID : `gb:${rawID}`)
    : provider === 'dnb' ? (rawID.startsWith('dnb:') ? rawID : `dnb:${rawID}`) : rawID
  if (['openlibrary', 'openlibrary_work', 'openlibrary_edition', 'ol'].includes(provider ?? '') && /^OL\d+[WMA]$/.test(id)) return metadataSourceLink(id, id.endsWith('A') ? 'author' : 'book')
  if ((provider === 'googlebooks' || provider === 'google') && /^gb:[A-Za-z0-9_-]+$/.test(id)) return metadataSourceLink(id, 'book')
  if (provider === 'hardcover' && /^hc:[a-z0-9][a-z0-9-]*$/i.test(id)) return metadataSourceLink(id, 'book')
  if (provider === 'dnb' && /^dnb:\d+$/.test(id)) return metadataSourceLink(id, 'book')
  return null
}

function Evidence({ values }: { values: CalibreAuditEvidence[] }) {
  const { t } = useTranslation()
  if (!values?.length) return <span className="text-fg-muted">{t('calibreAudit.noValue')}</span>
  return <ul className="space-y-1">{values.map((e, index) => {
    const source = e.source.startsWith('calibre.') ? 'calibre'
      : e.source.startsWith('editions.') ? 'edition'
      : e.source.startsWith('calibre_identity_evidence.') ? 'discovery'
      : e.source.startsWith('book_identifiers.') ? 'linkedIdentifier'
      : e.source.startsWith('authors.') ? 'author'
      : e.source.startsWith('series') ? 'series' : 'work'
    const record = providerRecordLink(e)
    const ownedIdentifier = e.source.startsWith('calibre.identifiers.')
    return <li key={index} className="break-words">
      <span className="font-medium">{record && ownedIdentifier
        ? <a href={record.url} target="_blank" rel="noopener noreferrer" className="text-emerald-700 dark:text-emerald-400 underline">{e.value} ↗</a>
        : e.value || t('calibreAudit.noValue')}</span>
      <span className="block text-xs text-fg-muted">
        {t(`calibreAudit.provenance.${source}`)}
        {e.provider && ` · ${providerDisplayName(e.provider)}`}
        {e.foreignId && <> · {record && !ownedIdentifier
          ? <a href={record.url} target="_blank" rel="noopener noreferrer" className="text-emerald-700 dark:text-emerald-400 underline">{e.foreignId} ↗</a>
          : e.foreignId}</>}
      </span>
    </li>
  })}</ul>
}

function IdentityContext({ finding }: { finding: CalibreAuditFinding }) {
  const { t } = useTranslation()
  const [snapshot, setSnapshot] = useState<CalibreIdentitySnapshot | null>(null)
  const [open, setOpen] = useState(false)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState(false)
  const toggle = () => {
    setOpen(!open)
    if (open || snapshot || loading) return
    setLoading(true)
    api.calibreAuditIdentity(finding.bookId)
      .then(data => { if (data.calibreId === finding.calibreId) setSnapshot(data); else setError(true) })
      .catch(() => setError(true))
      .finally(() => setLoading(false))
  }
  const root = snapshot?.evidence.find(e => e.status === 'root' && !e.editionId)
  const edition = snapshot?.evidence.find(e => e.editionId && (e.status === 'root' || e.status === 'corroborated') && e.editionConfidence !== 'unresolved')
  return <div className="text-xs text-fg-muted">
    <button type="button" onClick={toggle} aria-expanded={open} className="text-emerald-700 dark:text-emerald-400 underline">{t('calibreAudit.identityContext')}</button>
    {open && <p className="mt-1">{loading ? t('common.loading') : error ? t('calibreAudit.identityUnavailable') : snapshot
      ? t('calibreAudit.identitySummary', { work: root?.workConfidence || t('calibreAudit.unknown'), edition: edition?.editionConfidence || t('calibreAudit.unknown') })
      : t('calibreAudit.identityUnavailable')} {snapshot && t('calibreAudit.editionCaveat')}</p>}
  </div>
}

function Finding({ finding, cwaURL, busy, onIgnore, onReopen }: {
  finding: CalibreAuditFinding
  cwaURL: string
  busy: boolean
  onIgnore: (finding: CalibreAuditFinding) => void
  onReopen: (finding: CalibreAuditFinding) => void
}) {
  const { t } = useTranslation()
  const target = cwaBookURL(cwaURL, finding.calibreId)
  const historical = finding.state === 'unmatched'
  const editionID = finding.field === 'identifiers' && EDITION_IDS.has(finding.evidenceKey)
  const workID = finding.field === 'identifiers' && !editionID
  const current = finding.state === 'unresolved'
  const priority = historical ? 'historical' : current && finding.assessment === 'needs_review' ? 'actionable' : current ? 'uncertain' : finding.state
  return <li className={`rounded-lg p-4 space-y-3 border ${historical ? 'border-dashed border-slate-400 dark:border-zinc-600 opacity-80' : 'border-slate-300 dark:border-zinc-700'}`}>
    <div className="flex flex-wrap items-start justify-between gap-2">
      <div>
        <h3 className="font-semibold"><Link className="text-emerald-700 dark:text-emerald-400 underline" to={`/book/${finding.bookId}`}>{finding.bookTitle || `#${finding.bookId}`}</Link></h3>
        <p className="text-xs text-fg-muted">{t('calibreAudit.calibreId', { id: finding.calibreId })} · {t('calibreAudit.ownershipMatch', { confidence: finding.matchConfidence })}</p>
      </div>
      <div className="flex flex-wrap gap-2 text-xs">
        <span className="rounded bg-slate-200 dark:bg-zinc-800 px-2 py-1">{t(`calibreAudit.types.${finding.findingType}`)}</span>
        <span className={`rounded px-2 py-1 ${priority === 'actionable' ? 'bg-rose-100 text-rose-900 dark:bg-rose-950 dark:text-rose-200' : 'bg-slate-200 dark:bg-zinc-800'}`}>{t(`calibreAudit.priority.${priority}`)}</span>
        {editionID && <span className="rounded bg-amber-100 text-amber-900 dark:bg-amber-950 dark:text-amber-300 px-2 py-1">{t('calibreAudit.editionIdentifier')}</span>}
        {workID && <span className="rounded bg-sky-100 text-sky-900 dark:bg-sky-950 dark:text-sky-300 px-2 py-1">{t('calibreAudit.workIdentifier')}</span>}
        {finding.assessment === 'ambiguous' && !historical && <span className="rounded bg-amber-100 text-amber-900 dark:bg-amber-950 dark:text-amber-300 px-2 py-1">{t('calibreAudit.ambiguous')}</span>}
      </div>
    </div>
    {editionID && current && finding.assessment === 'ambiguous' && <p className="text-xs text-amber-700 dark:text-amber-400">{t('calibreAudit.editionHint')}</p>}
    {historical && <p className="text-sm text-fg-muted">{t('calibreAudit.unmatchedHint')}</p>}
    <div className="grid sm:grid-cols-2 gap-3 text-sm">
      <div><h4 className="font-semibold mb-1">{historical ? t('calibreAudit.priorOwned') : t('calibreAudit.owned')}</h4><Evidence values={finding.calibreEvidence} /></div>
      <div><h4 className="font-semibold mb-1">{historical ? t('calibreAudit.priorExternal') : t('calibreAudit.external')}</h4><Evidence values={finding.binderyEvidence} /></div>
    </div>
    {!historical && <p className="text-sm text-fg-muted">{t('calibreAudit.reason')}: {finding.reason}</p>}
    {!historical && <IdentityContext key={finding.comparisonFingerprint} finding={finding} />}
    {!!finding.decisions?.length && <details className="text-xs text-fg-muted"><summary className="cursor-pointer">{t('calibreAudit.decisionHistory')}</summary><ul className="mt-1 space-y-1">{finding.decisions.map((decision, i) => <li key={i}>{t(`calibreAudit.actions.${decision.action}`)} · {new Date(decision.createdAt).toLocaleString()}</li>)}</ul></details>}
    <div className="flex flex-wrap gap-3 text-sm">
      {current && <button disabled={busy} onClick={() => onIgnore(finding)} className="text-emerald-700 dark:text-emerald-400 underline disabled:opacity-50">{t('calibreAudit.ignore')}</button>}
      {finding.state === 'ignored' && <button disabled={busy} onClick={() => onReopen(finding)} className="text-emerald-700 dark:text-emerald-400 underline disabled:opacity-50">{t('calibreAudit.reopen')}</button>}
      {target && <a href={target} target="_blank" rel="noopener noreferrer" className="text-emerald-700 dark:text-emerald-400 underline">{t('calibreAudit.openCWA')}</a>}
    </div>
  </li>
}

export default function CalibreAuditPage() {
  const { t } = useTranslation()
  const [items, setItems] = useState<CalibreAuditFinding[]>([])
  const [total, setTotal] = useState(0)
  const [state, setState] = useState('unresolved')
  const [kind, setKind] = useState('')
  const [assessment, setAssessment] = useState('')
  const [identifierScope, setIdentifierScope] = useState('')
  const [cwaURL, setCwaURL] = useState('')
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [actionError, setActionError] = useState('')
  const [busy, setBusy] = useState(false)
  const [status, setStatus] = useState<CalibreAuditRecheckStatus | null>(null)
  const [statusLoading, setStatusLoading] = useState(true)
  const [statusError, setStatusError] = useState('')
  const [accepted, setAccepted] = useState(false)
  const [alreadyRunning, setAlreadyRunning] = useState(false)
  const [revision, setRevision] = useState(0)
  const { page, pageSize, paginationProps, reset } = useServerPagination(total, 50, 'calibre-audit')
  const refresh = useCallback(() => setRevision(n => n + 1), [])

  useEffect(() => {
    let active = true
    api.getSetting('cwa.web_url').then(s => { if (active) setCwaURL(s.value) }).catch(() => {})
    return () => { active = false }
  }, [])

  useEffect(() => {
    let active = true
    api.calibreAudit({ state: state || undefined, findingType: kind || undefined, assessment: assessment || undefined, identifierScope: identifierScope || undefined, limit: pageSize, offset: (page - 1) * pageSize })
      .then(data => { if (active) { setItems(data.items); setTotal(data.total); setError('') } })
      .catch(err => { if (active) { setItems([]); setError(err instanceof ApiError && err.status === 404 ? t('calibreAudit.disabled') : t('calibreAudit.loadError', { error: err instanceof Error ? err.message : String(err) })) } })
      .finally(() => { if (active) setLoading(false) })
    return () => { active = false }
  }, [state, kind, assessment, identifierScope, page, pageSize, revision, t])

  useEffect(() => {
    let active = true
    api.calibreAuditRecheckStatus()
      .then(next => { if (active) { setStatus(next); setStatusError('') } })
      .catch(err => { if (active) setStatusError(t('calibreAudit.statusError', { error: err instanceof Error ? err.message : String(err) })) })
      .finally(() => { if (active) setStatusLoading(false) })
    return () => { active = false }
  }, [t])

  useEffect(() => {
    if (!status?.running) return
    let active = true
    let inFlight = false
    const timer = window.setInterval(() => {
      if (inFlight) return
      inFlight = true
      api.calibreAuditRecheckStatus()
        .then(next => {
          if (!active) return
          setStatus(next); setStatusError('')
          if (!next.running) { setAccepted(false); setAlreadyRunning(false); refresh() }
        })
        .catch(err => { if (active) setStatusError(t('calibreAudit.statusError', { error: err instanceof Error ? err.message : String(err) })) })
        .finally(() => { inFlight = false })
    }, 2000)
    return () => { active = false; window.clearInterval(timer) }
  }, [status?.running, refresh, t])

  const filter = (set: (s: string) => void, value: string) => { set(value); reset(); setLoading(true) }
  const quickFilter = (nextState: string, nextAssessment: string, nextScope: string) => {
    setState(nextState); setKind(''); setAssessment(nextAssessment); setIdentifierScope(nextScope); reset(); setLoading(true)
  }
  const ignore = async (finding: CalibreAuditFinding) => {
    setBusy(true); setActionError('')
    try { await api.calibreAuditIgnore(finding.id, finding.comparisonFingerprint); refresh() }
    catch (err) {
      setActionError(err instanceof ApiError && err.status === 409 ? t('calibreAudit.stale') : t('calibreAudit.actionError', { error: err instanceof Error ? err.message : String(err) }))
      refresh()
    } finally { setBusy(false) }
  }
  const reopen = async (finding: CalibreAuditFinding) => {
    setBusy(true); setActionError('')
    try { await api.calibreAuditReopen(finding.id, finding.comparisonFingerprint); refresh() }
    catch (err) {
      setActionError(err instanceof ApiError && err.status === 409 ? t('calibreAudit.stale') : t('calibreAudit.actionError', { error: err instanceof Error ? err.message : String(err) }))
      refresh()
    } finally { setBusy(false) }
  }
  const recheck = async () => {
    setBusy(true); setActionError(''); setStatusError(''); setAccepted(false); setAlreadyRunning(false)
    try {
      const next = await api.calibreAuditRecheck()
      setStatus(next); setAccepted(true)
    } catch (err) {
      if (err instanceof ApiError && err.status === 409) {
        try {
          const next = await api.calibreAuditRecheckStatus()
          setStatus(next); setAlreadyRunning(next.running)
          if (!next.running) setActionError(t('calibreAudit.actionError', { error: err.message }))
        } catch (statusErr) {
          setStatusError(t('calibreAudit.statusError', { error: statusErr instanceof Error ? statusErr.message : String(statusErr) }))
        }
      } else {
        setActionError(t('calibreAudit.actionError', { error: err instanceof Error ? err.message : String(err) }))
      }
    } finally { setBusy(false) }
  }

  return <section className="space-y-5">
    <div className="flex flex-wrap justify-between gap-3">
      <div><h2 className="text-2xl font-bold">{t('calibreAudit.title')}</h2><p className="text-sm text-fg-muted">{t('calibreAudit.hint')}</p></div>
      <button onClick={recheck} disabled={busy || statusLoading || !!status?.running} className="self-start rounded bg-emerald-700 text-white px-3 py-2 text-sm disabled:opacity-50">{status?.running ? t('calibreAudit.working') : t('calibreAudit.recheck')}</button>
    </div>
    <nav aria-label={t('calibreAudit.quickFilters')} className="flex flex-wrap gap-2 text-sm">
      <button onClick={() => quickFilter('unresolved', 'needs_review', '')} className="rounded border px-2 py-1">{t('calibreAudit.currentQueue')}</button>
      <button onClick={() => quickFilter('unresolved', 'ambiguous', 'edition')} className="rounded border px-2 py-1">{t('calibreAudit.editionQueue')}</button>
      <button onClick={() => quickFilter('unmatched', '', '')} className="rounded border px-2 py-1">{t('calibreAudit.historicalQueue')}</button>
      <button onClick={() => quickFilter('ignored', '', '')} className="rounded border px-2 py-1">{t('calibreAudit.ignoredQueue')}</button>
    </nav>
    <div className="flex flex-wrap gap-3">
      <label className="text-sm">{t('calibreAudit.stateFilter')} <select value={state} onChange={e => filter(setState, e.target.value)} className="ml-1 rounded border p-1 bg-slate-100 dark:bg-zinc-800"><option value="">{t('common.all')}</option>{STATES.map(s => <option key={s} value={s}>{t(`calibreAudit.states.${s}`)}</option>)}</select></label>
      <label className="text-sm">{t('calibreAudit.typeFilter')} <select value={kind} onChange={e => filter(setKind, e.target.value)} className="ml-1 rounded border p-1 bg-slate-100 dark:bg-zinc-800"><option value="">{t('common.all')}</option>{TYPES.map(type => <option key={type} value={type}>{t(`calibreAudit.types.${type}`)}</option>)}</select></label>
      <label className="text-sm">{t('calibreAudit.assessmentFilter')} <select value={assessment} onChange={e => filter(setAssessment, e.target.value)} className="ml-1 rounded border p-1 bg-slate-100 dark:bg-zinc-800"><option value="">{t('common.all')}</option><option value="needs_review">{t('calibreAudit.needsReview')}</option><option value="ambiguous">{t('calibreAudit.ambiguous')}</option></select></label>
      <label className="text-sm">{t('calibreAudit.identifierScopeFilter')} <select value={identifierScope} onChange={e => filter(setIdentifierScope, e.target.value)} className="ml-1 rounded border p-1 bg-slate-100 dark:bg-zinc-800"><option value="">{t('common.all')}</option><option value="work">{t('calibreAudit.workIdentifier')}</option><option value="edition">{t('calibreAudit.editionIdentifier')}</option></select></label>
    </div>
    {actionError && <p role="alert" className="text-red-600 dark:text-red-400">{actionError}</p>}
    {statusError && <p role="alert" className="text-red-600 dark:text-red-400">{statusError}</p>}
    {status?.running && <p role="status">{alreadyRunning ? t('calibreAudit.alreadyRunning') : accepted ? t('calibreAudit.accepted') : t('calibreAudit.working')}</p>}
    {status?.error && <p role="alert" className="text-red-600 dark:text-red-400">{t('calibreAudit.actionError', { error: status.error })}</p>}
    {status?.result && !status.running && <p role="status">{t('calibreAudit.recheckResult', { compared: status.result.comparedBooks, findings: status.result.findings, updated: status.result.updated })}</p>}
    {!error && !loading && <p className="text-xs text-fg-muted">{t('calibreAudit.matchingCount', { count: total })}</p>}
    {loading ? <p role="status">{t('common.loading')}</p> : error ? <p role="alert" className="text-red-600 dark:text-red-400">{error}</p> : items.length === 0 ? <p>{t('calibreAudit.empty')}</p> : <ul className="space-y-3">{items.map(f => <Finding key={f.id} finding={f} cwaURL={cwaURL} busy={busy} onIgnore={ignore} onReopen={reopen} />)}</ul>}
    {!error && <Pagination {...paginationProps}
      onPageChange={next => { setLoading(true); paginationProps.onPageChange(next) }}
      onPageSizeChange={next => { setLoading(true); paginationProps.onPageSizeChange(next) }} />}
  </section>
}
