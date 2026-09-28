import { useCallback, useEffect, useState } from 'react'
import { Link } from 'react-router'
import { useTranslation } from 'react-i18next'
import {
  api,
  ApiError,
  type CalibreAuditEvidence,
  type CalibreAuditFinding,
  type CalibreAuditRecheckStatus,
  type CalibreIdentitySnapshot,
  type CalibreReconciliationStatus,
  type CalibreIdentifierProposal,
  type CalibreIdentifierAttempt,
} from '../api/client'
import { providerDisplayName } from '../util/metadataSource'
import CalibreIdentifierLink from '../components/CalibreIdentifierLink'
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
const WORK_IDS = new Set(['openlibrary', 'google', 'hardcover', 'dnb'])

function eligibleForIdentifierAdd(finding: CalibreAuditFinding): boolean {
  return finding.state === 'unresolved' && finding.assessment === 'needs_review' &&
    finding.findingType === 'identifier_missing' && finding.field === 'identifiers' &&
    WORK_IDS.has(finding.evidenceKey.toLowerCase())
}

function eligibleProposal(finding: CalibreAuditFinding, proposal: CalibreIdentifierProposal): boolean {
  return proposal.findingId === finding.id && proposal.bookId === finding.bookId &&
    proposal.calibreId === finding.calibreId && proposal.comparisonFingerprint === finding.comparisonFingerprint &&
    proposal.action === 'add' && !proposal.currentValue.trim() && !!proposal.proposedValue.trim() &&
    proposal.identifierType.toLowerCase() === finding.evidenceKey.toLowerCase() &&
    WORK_IDS.has(proposal.identifierType.toLowerCase())
}

function Evidence({ values, identifierType }: { values: CalibreAuditEvidence[]; identifierType?: string }) {
  const { t } = useTranslation()
  if (!values?.length) return <span className="text-fg-muted">{t('calibreAudit.noValue')}</span>
  return <ul className="space-y-1">{values.map((e, index) => {
    const source = e.source.startsWith('calibre.') ? 'calibre'
      : e.source.startsWith('editions.') ? 'edition'
      : e.source.startsWith('calibre_identity_evidence.') ? 'discovery'
      : e.source.startsWith('book_identifiers.') ? 'linkedIdentifier'
      : e.source.startsWith('authors.') ? 'author'
      : e.source.startsWith('series') ? 'series' : 'work'
    const valueType = e.source.startsWith('calibre.identifiers.') ? e.source.slice('calibre.identifiers.'.length)
      : ['books.foreign_id', 'book_identifiers.foreign_id', 'editions.foreign_id'].includes(e.source) ? 'foreign_id'
      : identifierType ?? ''
    return <li key={index} className="break-words">
      <span className="font-medium">{e.value
        ? <CalibreIdentifierLink type={valueType} value={e.value} provider={e.provider} />
        : t('calibreAudit.noValue')}</span>
      <span className="block text-xs text-fg-muted">
        {t(`calibreAudit.provenance.${source}`)}
        {e.provider && ` · ${providerDisplayName(e.provider)}`}
        {e.foreignId && <> · <CalibreIdentifierLink type="foreign_id" value={e.foreignId} provider={e.provider} /></>}
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
  const resolution = snapshot?.edition
  const edition = resolution?.editionId ? `${resolution.confidence} (\uFFF0)` : resolution?.confidence || t('calibreAudit.unknown')
  const [summaryBefore, summaryAfter] = t('calibreAudit.identitySummary', {
    work: root?.workConfidence || t('calibreAudit.unknown'), edition,
  }).split('\uFFF0')
  return <div className="text-xs text-fg-muted">
    <button type="button" onClick={toggle} aria-expanded={open} className="text-emerald-700 dark:text-emerald-400 underline">{t('calibreAudit.identityContext')}</button>
    {open && <div className="mt-1">{loading ? t('common.loading') : error ? t('calibreAudit.identityUnavailable') : snapshot
      ? <>
          <p>{summaryBefore}{resolution?.editionId && <CalibreIdentifierLink type="foreign_id" value={resolution.editionId} provider={resolution.provider} />}{summaryAfter}</p>
          {resolution?.reason && <p>{resolution.reason}</p>}
          {(resolution?.confidence === 'ambiguous' || resolution?.confidence === 'unresolved') && <p>{t('calibreAudit.editionCaveat')}</p>}
          {resolution?.candidates?.length ? <ul>{resolution.candidates.map(candidate =>
            <li key={`${candidate.provider}:${candidate.editionId}`}>
              {candidate.provider}: <CalibreIdentifierLink type="foreign_id" value={candidate.editionId} provider={candidate.provider} />
              {candidate.reasons.length ? ` — ${candidate.reasons.join('; ')}` : ''}
              {candidate.claims?.length ? <> · {candidate.claims.map((claim, index) =>
                <span key={`${claim.type}:${claim.value}`}>{index > 0 && ', '}{claim.type}:<CalibreIdentifierLink type={claim.type} value={claim.value} /></span>
              )}</> : null}
            </li>
          )}</ul> : null}
        </>
      : t('calibreAudit.identityUnavailable')}</div>}
  </div>
}

function IdentifierAdd({ finding, busy, onRefresh, onNotice }: { finding: CalibreAuditFinding; busy: boolean; onRefresh: () => void; onNotice: (outcome: string, error: string) => void }) {
  const { t } = useTranslation()
  const [proposals, setProposals] = useState<CalibreIdentifierProposal[] | null>(null)
  const [selected, setSelected] = useState<number | null>(null)
  const [approved, setApproved] = useState(false)
  const [loading, setLoading] = useState(false)
  const [applying, setApplying] = useState(false)
  const [error, setError] = useState('')

  const preview = async () => {
    setLoading(true); setError(''); onNotice('', ''); setProposals(null); setSelected(null); setApproved(false)
    try {
      const data = await api.calibreAuditIdentifierProposals(finding.id)
      if (data.items.some(item => item.comparisonFingerprint !== finding.comparisonFingerprint || item.findingId !== finding.id)) {
        onNotice('', t('calibreAudit.stale')); onRefresh(); return
      }
      setProposals(data.items.filter(item => eligibleProposal(finding, item)))
    } catch (err) {
      const message = err instanceof ApiError && err.status === 409 ? t('calibreAudit.stale') : t('calibreAudit.proposalError', { error: err instanceof Error ? err.message : String(err) })
      if (err instanceof ApiError && err.status === 409) { onNotice('', message); onRefresh() }
      else setError(message)
    } finally { setLoading(false) }
  }
  const apply = async () => {
    const item = selected === null ? null : proposals?.[selected]
    if (!approved || !item || !eligibleProposal(finding, item) || applying || busy) return
    setApplying(true); setError(''); onNotice('', '')
    try {
      const response = await api.calibreAuditIdentifierAdd(finding.id, {
        comparisonFingerprint: item.comparisonFingerprint, proposedValue: item.proposedValue,
      })
      setProposals(null); setApproved(false); setSelected(null)
      onNotice(t('calibreAudit.addResult', { outcome: response.outcome, id: response.attemptId }),
        response.reauditError ? t('calibreAudit.reauditError', { error: response.reauditError }) : '')
      onRefresh()
    } catch (err) {
      setProposals(null); setApproved(false); setSelected(null)
      const message = err instanceof ApiError && err.status === 409 ? t('calibreAudit.stale')
        : t('calibreAudit.addError', { error: err instanceof Error ? err.message : String(err) })
      const id = err instanceof ApiError ? err.body.attemptId : undefined
      onNotice('', message + (typeof id === 'number' && id > 0 ? ` ${t('calibreAudit.addAttemptReference', { id })}` : ''))
      onRefresh()
    } finally { setApplying(false) }
  }

  return <div className="rounded border border-sky-300 dark:border-sky-800 p-3 space-y-3 text-sm">
    <button type="button" disabled={busy || loading || applying} onClick={preview} className="text-emerald-700 dark:text-emerald-400 underline disabled:opacity-50">{t('calibreAudit.previewAdd')}</button>
    {loading && <p role="status">{t('common.loading')}</p>}
    {error && <p role="alert" className="text-red-600 dark:text-red-400">{error}</p>}
    {proposals && <div className="space-y-3">
      <p className="text-fg-muted">{t('calibreAudit.addCaution', { confidence: finding.matchConfidence })}</p>
      {proposals.length === 0 ? <p>{t('calibreAudit.noProposals')}</p> : <>
        <fieldset className="space-y-3">
          <legend className="font-semibold">{t('calibreAudit.selectAddition')}</legend>
          {proposals.map((item, index) =>
            <div key={`${item.identifierType}-${item.proposedValue}-${index}`} className="rounded border border-slate-300 dark:border-zinc-700 p-2 space-y-2">
              <label className="flex items-center gap-2 break-all"><input type="radio" name={`identifier-add-${finding.id}`} checked={selected === index} onChange={() => { setSelected(index); setApproved(false) }} />{item.identifierType} · <CalibreIdentifierLink type={item.identifierType} value={item.proposedValue} /></label>
              <p>{t('calibreAudit.existingValue')}: {item.currentValue ? <CalibreIdentifierLink type={item.identifierType} value={item.currentValue} /> : t('calibreAudit.noValue')}</p>
              <p>{t('calibreAudit.proposedValue')}: <CalibreIdentifierLink type={item.identifierType} value={item.proposedValue} /></p>
              <p>{t('calibreAudit.reason')}: {item.reason}</p>
              <div><span className="font-medium">{t('calibreAudit.proposalEvidence')}</span><Evidence values={item.evidence} identifierType={item.identifierType} /></div>
            </div>
          )}
        </fieldset>
        <label className="flex items-start gap-2"><input type="checkbox" checked={approved} disabled={selected === null || busy || applying} onChange={e => setApproved(e.target.checked)} />{t('calibreAudit.approveAdd')}</label>
        <button type="button" disabled={!approved || selected === null || busy || applying} onClick={apply} className="rounded bg-emerald-700 text-white px-3 py-2 disabled:opacity-50">{t('calibreAudit.applyAdd')}</button>
      </>}
    </div>}
  </div>
}

function IdentifierAttemptHistory({ findingId, revision }: { findingId: number; revision: number }) {
  const { t } = useTranslation()
  const [open, setOpen] = useState(false)
  const [items, setItems] = useState<CalibreIdentifierAttempt[] | null>(null)
  const [error, setError] = useState('')
  useEffect(() => {
    if (!open) return
    let active = true
    api.calibreAuditIdentifierAttempts(findingId)
      .then(data => { if (active) { setItems(data.items); setError('') } })
      .catch(err => { if (active) setError(t('calibreAudit.attemptHistoryError', { error: err instanceof Error ? err.message : String(err) })) })
    return () => { active = false }
  }, [open, findingId, revision, t])
  return <div className="text-xs">
    <button type="button" onClick={() => setOpen(!open)} aria-expanded={open} className="text-emerald-700 dark:text-emerald-400 underline">{t('calibreAudit.attemptHistory')}</button>
    {open && <div className="mt-2 space-y-1">
      {error && <p role="alert">{error}</p>}
      {items?.length === 0 && <p>{t('calibreAudit.noAttempts')}</p>}
      {items?.map(item => {
        const [before, after] = t('calibreAudit.attemptSummary', {
          id: item.id, action: item.action, field: item.identifierType, value: '\uFFF0', outcome: item.outcome,
        }).split('\uFFF0')
        return <p key={item.id} className="break-words">{before}<CalibreIdentifierLink type={item.identifierType} value={item.proposedValue} />{after} {item.error && t('calibreAudit.attemptFailure', { error: item.error })}</p>
      })}
    </div>}
  </div>
}

function Finding({ finding, cwaURL, busy, identifierWriteEnabled, revision, onRefresh, onNotice, onIgnore, onReopen }: {
  finding: CalibreAuditFinding
  cwaURL: string
  busy: boolean
  identifierWriteEnabled: boolean
  revision: number
  onRefresh: () => void
  onNotice: (outcome: string, error: string) => void
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
      <div><h4 className="font-semibold mb-1">{historical ? t('calibreAudit.priorOwned') : t('calibreAudit.owned')}</h4><Evidence values={finding.calibreEvidence} identifierType={finding.field === 'identifiers' ? finding.evidenceKey : undefined} /></div>
      <div><h4 className="font-semibold mb-1">{historical ? t('calibreAudit.priorExternal') : t('calibreAudit.external')}</h4><Evidence values={finding.binderyEvidence} identifierType={finding.field === 'identifiers' ? finding.evidenceKey : undefined} /></div>
    </div>
    {!historical && <p className="text-sm text-fg-muted">{t('calibreAudit.reason')}: {finding.reason}</p>}
    {!historical && <IdentityContext key={finding.comparisonFingerprint} finding={finding} />}
    {identifierWriteEnabled && eligibleForIdentifierAdd(finding) && <IdentifierAdd key={finding.comparisonFingerprint} finding={finding} busy={busy} onRefresh={onRefresh} onNotice={onNotice} />}
    <IdentifierAttemptHistory findingId={finding.id} revision={revision} />
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
  const [identifierWriteEnabled, setIdentifierWriteEnabled] = useState(false)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [actionError, setActionError] = useState('')
  const [identifierOutcome, setIdentifierOutcome] = useState('')
  const [identifierError, setIdentifierError] = useState('')
  const identifierNotice = (outcome: string, error: string) => { setIdentifierOutcome(outcome); setIdentifierError(error) }
  const [busy, setBusy] = useState(false)
  const [status, setStatus] = useState<CalibreAuditRecheckStatus | null>(null)
  const [reconciliation, setReconciliation] = useState<CalibreReconciliationStatus | null>(null)
  const [reconciliationLoading, setReconciliationLoading] = useState(true)
  const [reconciliationBusy, setReconciliationBusy] = useState(false)
  const [reconciliationError, setReconciliationError] = useState('')
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
    // A missing/unreadable setting is not consent to write; this opt-in is
    // independent of Calibre's read-only authoritative audit mode.
    api.getSetting('calibre.identifier_write_enabled')
      .then(s => { if (active) setIdentifierWriteEnabled(s.value.toLowerCase() === 'true') })
      .catch(() => { if (active) setIdentifierWriteEnabled(false) })
    return () => { active = false }
  }, [revision])

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

  useEffect(() => {
    let active = true
    api.calibreReconcileStatus()
      .then(next => { if (active) { setReconciliation(next); setReconciliationError('') } })
      .catch(err => { if (active) setReconciliationError(t('calibreAudit.reconciliation.statusError', { error: err instanceof Error ? err.message : String(err) })) })
      .finally(() => { if (active) setReconciliationLoading(false) })
    return () => { active = false }
  }, [t])

  useEffect(() => {
    if (reconciliation?.state !== 'running') return
    let active = true
    let inFlight = false
    const timer = window.setInterval(() => {
      if (inFlight) return
      inFlight = true
      api.calibreReconcileStatus()
        .then(next => {
          if (!active) return
          setReconciliation(next); setReconciliationError('')
          if (next.state !== 'running') refresh()
        })
        .catch(err => { if (active) setReconciliationError(t('calibreAudit.reconciliation.statusError', { error: err instanceof Error ? err.message : String(err) })) })
        .finally(() => { inFlight = false })
    }, 2000)
    return () => { active = false; window.clearInterval(timer) }
  }, [reconciliation?.state, refresh, t])

  const runReconciliation = async () => {
    setReconciliationBusy(true); setReconciliationError('')
    try {
      setReconciliation(await api.calibreReconcile())
    } catch (err) {
      if (err instanceof ApiError && err.status === 409) {
        try { setReconciliation(await api.calibreReconcileStatus()) }
        catch (statusErr) { setReconciliationError(t('calibreAudit.reconciliation.statusError', { error: statusErr instanceof Error ? statusErr.message : String(statusErr) })) }
      } else {
        setReconciliationError(t('calibreAudit.reconciliation.startError', { error: err instanceof Error ? err.message : String(err) }))
      }
    } finally { setReconciliationBusy(false) }
  }

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
      <div className="flex flex-wrap gap-2">
        <button onClick={runReconciliation} disabled={reconciliationBusy || busy || reconciliationLoading || statusLoading || reconciliation?.state === 'running' || !!status?.running} className="self-start rounded bg-emerald-700 text-white px-3 py-2 text-sm disabled:opacity-50">{reconciliation?.state === 'running' ? t('calibreAudit.reconciliation.running') : t('calibreAudit.reconciliation.run')}</button>
        <button onClick={recheck} disabled={busy || statusLoading || reconciliationLoading || !!status?.running || reconciliation?.state === 'running'} className="self-start rounded border px-3 py-2 text-sm disabled:opacity-50">{status?.running ? t('calibreAudit.working') : t('calibreAudit.recheck')}</button>
      </div>
    </div>
    <p className="text-xs text-fg-muted">{t('calibreAudit.reconciliation.scope')}</p>
    {reconciliationError && <p role="alert" className="text-red-600 dark:text-red-400">{reconciliationError}</p>}
    {reconciliation?.state === 'running' && <p role="status">{t('calibreAudit.reconciliation.stage', { stage: t(`calibreAudit.reconciliation.stages.${reconciliation.stage ?? 'ownership'}`) })} {t('calibreAudit.reconciliation.completedStages', { stages: reconciliation.completedStages.map(s => t(`calibreAudit.reconciliation.stages.${s}`)).join(', ') || t('calibreAudit.reconciliation.none') })}</p>}
    {reconciliation?.state === 'failed' && <p role="alert" className="text-red-600 dark:text-red-400">{t('calibreAudit.reconciliation.failed', { error: reconciliation.error })}</p>}
    {reconciliation?.state === 'partial' && <p role="alert" className="text-amber-700 dark:text-amber-400">{t('calibreAudit.reconciliation.partial')}</p>}
    {reconciliation && ['completed', 'partial'].includes(reconciliation.state) && <p role="status">{t('calibreAudit.reconciliation.complete')}</p>}
    {reconciliation?.result && <div className="rounded border p-3 space-y-1 text-sm" aria-label={t('calibreAudit.reconciliation.summary')}>
      <p>{t('calibreAudit.reconciliation.ownership', { total: reconciliation.result.totalBinderyBooks, calibre: reconciliation.result.totalCalibreBooks, matched: reconciliation.result.matched, revalidated: reconciliation.result.revalidated, stale: reconciliation.result.stale, unmatched: reconciliation.result.unmatched })}</p>
      {reconciliation.result.identity && <>
        <p>{t('calibreAudit.reconciliation.identity', { works: reconciliation.result.identity.refreshedWorks, records: reconciliation.result.identity.evidenceRecords })}</p>
        <p>{t('calibreAudit.reconciliation.providers', { failed: reconciliation.result.identity.failedLookups, truncated: reconciliation.result.identity.truncatedLookups, unconfigured: reconciliation.result.identity.unconfiguredLookups, skipped: reconciliation.result.identity.notAttemptedLookups, deferred: reconciliation.result.identity.deferredWorks, unresolved: reconciliation.result.identity.unresolvedRoots, unavailable: reconciliation.result.identity.discoveryUnavailable ? t('calibreAudit.reconciliation.unavailable') : t('calibreAudit.reconciliation.available') })}</p>
      </>}
      <p>{t('calibreAudit.reconciliation.artifacts', { scans: reconciliation.result.artifactScansCached })}</p>
      {reconciliation.result.audit && <p>{t('calibreAudit.reconciliation.audit', { compared: reconciliation.result.audit.comparedBooks, findings: reconciliation.result.audit.findings, updated: reconciliation.result.audit.updated })}</p>}
      {reconciliation.result.transitions && <p>{t('calibreAudit.reconciliation.transitions', { new: reconciliation.result.transitions.newUnresolved, resolved: reconciliation.result.transitions.resolved, ignored: reconciliation.result.transitions.ignoredPreserved, historical: reconciliation.result.transitions.becameHistorical })}</p>}
    </div>}
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
    {identifierOutcome && <p role="status" aria-label={t('calibreAudit.addStatus')}>{identifierOutcome}</p>}
    {identifierError && <p role="alert" className="text-red-600 dark:text-red-400">{identifierError}</p>}
    {statusError && <p role="alert" className="text-red-600 dark:text-red-400">{statusError}</p>}
    {status?.running && <p role="status">{alreadyRunning ? t('calibreAudit.alreadyRunning') : accepted ? t('calibreAudit.accepted') : t('calibreAudit.working')}</p>}
    {status?.error && <p role="alert" className="text-red-600 dark:text-red-400">{t('calibreAudit.actionError', { error: status.error })}</p>}
    {status?.result && !status.running && <p role="status">{t('calibreAudit.recheckResult', { compared: status.result.comparedBooks, findings: status.result.findings, updated: status.result.updated })}</p>}
    {!error && !loading && <p className="text-xs text-fg-muted">{t('calibreAudit.matchingCount', { count: total })}</p>}
    {loading ? <p role="status">{t('common.loading')}</p> : error ? <p role="alert" className="text-red-600 dark:text-red-400">{error}</p> : items.length === 0 ? <p>{t('calibreAudit.empty')}</p> : <ul className="space-y-3">{items.map(f => <Finding key={f.id} finding={f} cwaURL={cwaURL} busy={busy} identifierWriteEnabled={identifierWriteEnabled} revision={revision} onRefresh={refresh} onNotice={identifierNotice} onIgnore={ignore} onReopen={reopen} />)}</ul>}
    {!error && <Pagination {...paginationProps}
      onPageChange={next => { setLoading(true); paginationProps.onPageChange(next) }}
      onPageSizeChange={next => { setLoading(true); paginationProps.onPageSizeChange(next) }} />}
  </section>
}
