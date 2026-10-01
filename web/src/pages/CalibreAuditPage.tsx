import { useCallback, useEffect, useRef, useState } from 'react'
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
import { apiURL } from '../api/core'
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

// This is the page's single gate for offering an identifier addition. It is
// deliberately narrower than the server's final live revalidation.
type IdentifierAddBlock = 'disabled' | 'notCurrent' | 'conflict' | 'unsupported' | 'edition' | 'ambiguous'
function identifierAddBlock(finding: CalibreAuditFinding, enabled: boolean): IdentifierAddBlock | null {
  if (!enabled) return 'disabled'
  if (finding.state !== 'unresolved') return 'notCurrent'
  if (finding.findingType !== 'identifier_missing') return finding.findingType === 'identifier_conflict' ? 'conflict' : 'unsupported'
  if (finding.field !== 'identifiers') return 'unsupported'
  const type = finding.evidenceKey.toLowerCase()
  if (!WORK_IDS.has(type)) return EDITION_IDS.has(type) ? 'edition' : 'unsupported'
  if (finding.assessment !== 'needs_review') return 'ambiguous'
  return null
}

// Only the resolver's category can explain its decision; raw observations
// cannot establish which evidence won when sources overlap.
function editionExplanation(confidence?: string, reasonCode?: string): string {
  if (confidence === 'exact') return reasonCode === 'independent_file_isbn' ? 'independentFile' : 'exact'
  if (confidence === 'high') {
    if (reasonCode === 'calibre_identifiers') return 'calibreClaim'
    if (reasonCode === 'calibre_metadata') return 'calibreMetadata'
    if (reasonCode === 'historical_file_isbn') return 'historicalFile'
    return 'likely'
  }
  if (confidence === 'ambiguous') {
    if (reasonCode === 'lookup_incomplete') return 'lookupIncomplete'
    if (reasonCode === 'conflicting_evidence') return 'conflicting'
    return 'multiple'
  }
  return 'unresolved'
}

function eligibleProposal(finding: CalibreAuditFinding, proposal: CalibreIdentifierProposal): boolean {
  return proposal.findingId === finding.id && proposal.bookId === finding.bookId &&
    proposal.calibreId === finding.calibreId && proposal.comparisonFingerprint === finding.comparisonFingerprint &&
    proposal.action === 'add' && !proposal.currentValue.trim() && !!proposal.proposedValue.trim() &&
    proposal.identifierType.toLowerCase() === finding.evidenceKey.toLowerCase() &&
    WORK_IDS.has(proposal.identifierType.toLowerCase())
}

function evidenceValueType(e: CalibreAuditEvidence, identifierType?: string): string {
  return e.source.startsWith('calibre.identifiers.') ? e.source.slice('calibre.identifiers.'.length)
    : ['books.foreign_id', 'book_identifiers.foreign_id', 'editions.foreign_id'].includes(e.source) ? 'foreign_id'
    : identifierType ?? ''
}

function evidenceSource(e: CalibreAuditEvidence): string {
  return e.source.startsWith('calibre.') ? 'calibre'
    : e.source.startsWith('calibre_identity_evidence.') ? 'discovery'
    : 'storedMetadata'
}

function Evidence({ values, identifierType }: { values: CalibreAuditEvidence[]; identifierType?: string }) {
  const { t } = useTranslation()
  if (!values?.length) return <span className="text-fg-muted">{t('calibreAudit.noValue')}</span>
  // Group only literal, typed claims for display. The server keeps every
  // observation and its fingerprint; equivalent ISBN forms remain visible.
  const groups: Array<{ value: string; type: string; linkType: string; observations: CalibreAuditEvidence[] }> = []
  for (const e of values) {
    const linkType = evidenceValueType(e, identifierType)
    const type = linkType === 'foreign_id' ? identifierType ?? linkType : linkType
    const group = identifierType && e.value ? groups.find(g => g.type === type && g.value === e.value) : undefined
    if (group) group.observations.push(e)
    else groups.push({ value: e.value, type, linkType, observations: [e] })
  }
  return <ul className="space-y-1">{groups.map((group, index) => {
    const e = group.observations[0]
    const provenances = group.observations.filter((observation, position, all) =>
      all.findIndex(other => evidenceSource(other) === evidenceSource(observation) &&
        other.provider === observation.provider && other.foreignId === observation.foreignId) === position)
    const isbnForm = identifierType === 'isbn' && group.value
      ? group.value.replace(/[-\s]/g, '').length : 0
    return <li key={index} className="break-words">
      <span className="font-medium">
        {(isbnForm === 10 || isbnForm === 13) && <>{t(`calibreAudit.identifierNames.isbn${isbnForm}`)} · </>}
        {group.value ? <CalibreIdentifierLink type={group.linkType} value={group.value} provider={e.provider} /> : t('calibreAudit.noValue')}
      </span>
      {group.value && provenances.map((observation, position) => <span key={position} className="block text-xs text-fg-muted">
        {t(`calibreAudit.provenance.${evidenceSource(observation)}`)}
        {observation.provider && ` · ${providerDisplayName(observation.provider)}`}
        {observation.foreignId && observation.foreignId !== group.value &&
          <> · <CalibreIdentifierLink type="foreign_id" value={observation.foreignId} provider={observation.provider} /></>}
      </span>)}
    </li>
  })}</ul>
}

function candidateCoverURL(snapshot: CalibreIdentitySnapshot, provider: string, editionId: string): string | null {
  // Only the rooted exact-edition record of this work can supply artwork for
  // this candidate. Never borrow a work cover, CWA claim, or search hit.
  const records = snapshot.evidence.filter(e => e.status === 'root' && e.method === 'exact_editions' &&
    e.canonicalIdentity === snapshot.rootKey && e.provider === provider && e.editionId === editionId &&
    e.providerMetadata?.ebook === true)
  if (records.length !== 1 || typeof records[0].providerMetadata?.imageUrl !== 'string') return null
  const raw = records[0].providerMetadata.imageUrl
  try {
    const url = new URL(raw)
    if (raw.length > 2048 || url.protocol !== 'https:' || !url.hostname || url.username || url.password || url.hash) return null
    // The existing image proxy enforces strict SSRF and caches only image bytes.
    return apiURL(`/images?url=${encodeURIComponent(raw)}`)
  } catch { return null }
}

function CoverThumbnail({ src, alt }: { src: string; alt: string }) {
  const [failed, setFailed] = useState(false)
  if (failed) return null
  return <img src={src} alt={alt} loading="lazy" decoding="async" width="80" height="112"
    className="h-28 w-20 shrink-0 rounded border border-slate-300 dark:border-zinc-700 object-contain"
    onError={() => setFailed(true)} />
}

function IdentityContext({ finding, loadIdentity }: { finding: CalibreAuditFinding; loadIdentity: (bookId: number) => Promise<CalibreIdentitySnapshot> }) {
  const { t } = useTranslation()
  const [snapshot, setSnapshot] = useState<CalibreIdentitySnapshot | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState(false)
  useEffect(() => {
    let active = true
    loadIdentity(finding.bookId)
      .then(data => { if (active) { if (data.calibreId === finding.calibreId) setSnapshot(data); else setError(true) } })
      .catch(() => { if (active) setError(true) })
      .finally(() => { if (active) setLoading(false) })
    return () => { active = false }
  }, [finding.bookId, finding.calibreId, loadIdentity])
  const resolution = snapshot?.edition
  const confidence = resolution?.confidence
  const explanation = editionExplanation(confidence, resolution?.reasonCode)
  // The selected CWA claim is book-level evidence, not an explanation of any
  // one missing identifier comparison. Never infer the basis from raw evidence.
  const selectedClaim = explanation === 'calibreClaim' ? resolution?.candidates?.find(candidate =>
    candidate.editionId === resolution.editionId && candidate.provider === resolution.provider)?.claims?.[0] : undefined
  return <section className="space-y-2 text-sm" aria-label={t('calibreAudit.editionHeading')}>
    <h4 className="border-b border-slate-200 dark:border-zinc-700 pb-1 text-base font-semibold">{t('calibreAudit.editionHeading')}</h4>
    {loading ? <p className="text-fg-muted">{t('common.loading')}</p> : error || !snapshot
      ? <p className="text-fg-muted">{t('calibreAudit.identityUnavailable')}</p> : <>
        <p>{t('calibreAudit.editionStatus.' + (confidence || 'unresolved'))}{resolution?.editionId &&
          <> · {providerDisplayName(resolution.provider || '')} <CalibreIdentifierLink type="foreign_id" value={resolution.editionId} provider={resolution.provider} /></>}</p>
        <p className="text-fg-muted">{t('calibreAudit.editionExplanation.' + (selectedClaim ? 'calibreClaimTyped' : explanation), {
          sourceIdentifier: selectedClaim && t(`calibreAudit.identifierNames.${selectedClaim.type}`, { defaultValue: selectedClaim.type }),
        })}</p>
        {resolution?.artifactWarning === 'possibly_calibre_derived' && <p className="text-fg-muted">{t('calibreAudit.possiblyDerived')}</p>}
        <div className="grid gap-3 sm:grid-cols-2">
        <div className="rounded border border-slate-300 dark:border-zinc-700 p-2" aria-label={t('calibreAudit.currentCopy')}>
          <p className="font-medium">{t('calibreAudit.currentCopy')} · {t('calibreAudit.calibreId', { id: finding.calibreId })}</p>
          <div className="flex gap-3 mt-1">
            {snapshot.hasOwnedCover && <CoverThumbnail src={api.calibreOwnedCoverURL(finding.bookId)} alt={t('calibreAudit.currentCover')} />}
            <p className="text-xs text-fg-muted">{t('calibreAudit.currentDetails')}</p>
          </div>
        </div>
        {!!resolution?.candidates?.length && <div>
          {(confidence === 'ambiguous' || confidence === 'unresolved') && <>
            <p className="font-medium">{t('calibreAudit.candidates')}</p>
            <p className="text-fg-muted">{t('calibreAudit.candidateCaveat')}</p>
          </>}
          <ul className="mt-2 grid gap-2">{resolution.candidates.filter(candidate =>
            confidence === 'ambiguous' || confidence === 'unresolved' ||
            (candidate.editionId === resolution.editionId && candidate.provider === resolution.provider)
          ).map(candidate => {
            const record = snapshot.evidence.find(e => e.status === 'root' && e.method === 'exact_editions' &&
              e.canonicalIdentity === snapshot.rootKey && e.provider === candidate.provider && e.editionId === candidate.editionId)
            const title = record?.providerMetadata?.title || snapshot.evidence.find(e =>
              e.editionId === candidate.editionId && e.provider === candidate.provider)?.providerMetadata?.title
            const image = candidateCoverURL(snapshot, candidate.provider, candidate.editionId)
            return <li key={candidate.provider + ':' + candidate.editionId} className="flex gap-3 rounded border border-slate-300 dark:border-zinc-700 p-2">
              {image && <CoverThumbnail src={image} alt={t('calibreAudit.candidateCover', { provider: providerDisplayName(candidate.provider), id: candidate.editionId })} />}
              <div className="min-w-0 break-words">
                <p className="font-medium">{title || providerDisplayName(candidate.provider)} · <CalibreIdentifierLink type="foreign_id" value={candidate.editionId} provider={candidate.provider} /></p>
                {record?.providerMetadata?.publisher && <p>{record.providerMetadata.publisher}</p>}
                {record?.providerMetadata?.publicationDate && <p>{record.providerMetadata.publicationDate}</p>}
              </div>
            </li>
          })}</ul>
          <p className="text-xs text-fg-muted">{t('calibreAudit.coverCaveat')}</p>
        </div>}
        </div>
        <details className="text-xs text-fg-muted"><summary className="cursor-pointer">{t('calibreAudit.editionDetails')}</summary>
          <p>{t('calibreAudit.rawConfidence')}: {confidence || t('calibreAudit.unknown')} · {resolution?.reasonCode || t('calibreAudit.unknown')}</p>
          {snapshot.rootKey && <p>{t('calibreAudit.rootKey')}: {snapshot.rootKey}</p>}
          {resolution?.reason && <p>{t('calibreAudit.rawReason')}: {resolution.reason}</p>}
          {resolution?.candidates?.map(candidate => <div key={candidate.provider + ':' + candidate.editionId}>
            <p>{candidate.provider}: <CalibreIdentifierLink type="foreign_id" value={candidate.editionId} provider={candidate.provider} />{candidate.reasons.length ? ' — ' + candidate.reasons.join('; ') : ''}</p>
            {candidate.claims?.length ? <p>{candidate.claims.map((claim, index) =>
              <span key={claim.type + ':' + claim.value}>{index > 0 && ', '}{claim.type}: <CalibreIdentifierLink type={claim.type} value={claim.value} /></span>
            )}</p> : null}
          </div>)}
          {snapshot.lookups?.map((lookup, index) => <p key={index}>{lookup.provider} · {lookup.method}: {lookup.outcome}</p>)}
          {snapshot.artifacts?.map((scan, index) => <p key={index}>{t('calibreAudit.fileLineage')}: {scan.lineage}</p>)}
        </details>
      </>}
  </section>
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
      <p className="text-fg-muted">{t('calibreAudit.addCaution')}</p>
      {proposals.length === 0 ? <p>{t('calibreAudit.noProposals')}</p> : <>
        <fieldset className="space-y-3">
          <legend className="font-semibold">{t('calibreAudit.selectAddition')}</legend>
          {proposals.map((item, index) =>
            <div key={item.identifierType + '-' + item.proposedValue + '-' + index} className="rounded border border-slate-300 dark:border-zinc-700 p-2 space-y-2">
              <label className="flex items-center gap-2 break-all"><input type="radio" name={`identifier-add-${finding.id}`} checked={selected === index} onChange={() => { setSelected(index); setApproved(false) }} />{item.identifierType} · <CalibreIdentifierLink type={item.identifierType} value={item.proposedValue} /></label>
              <p>{t('calibreAudit.existingValue')}: {item.currentValue ? <CalibreIdentifierLink type={item.identifierType} value={item.currentValue} /> : t('calibreAudit.noValue')}</p>
              <p>{t('calibreAudit.proposedValue')}: <CalibreIdentifierLink type={item.identifierType} value={item.proposedValue} /></p>
              <p>{t('calibreAudit.proposalExplanation')}</p>
              <div><span className="font-medium">{t('calibreAudit.proposalEvidence')}</span><Evidence values={item.evidence} identifierType={item.identifierType} /></div>
              <details className="text-xs text-fg-muted"><summary>{t('calibreAudit.technicalDetails')}</summary>{item.reason}</details>
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

type FindingProps = {
  finding: CalibreAuditFinding
  busy: boolean
  identifierWriteEnabled: boolean
  revision: number
  onRefresh: () => void
  onNotice: (outcome: string, error: string) => void
  onIgnore: (finding: CalibreAuditFinding) => void
  onReopen: (finding: CalibreAuditFinding) => void
}

function Finding({ finding, busy, identifierWriteEnabled, revision, onRefresh, onNotice, onIgnore, onReopen }: FindingProps) {
  const { t } = useTranslation()
  const historical = finding.state === 'unmatched'
  const editionID = finding.field === 'identifiers' && EDITION_IDS.has(finding.evidenceKey)
  const workID = finding.field === 'identifiers' && !editionID
  const current = finding.state === 'unresolved'
  const priority = historical ? 'historical' : current && finding.assessment === 'needs_review' ? 'actionable' : current ? 'uncertain' : finding.state
  const addBlock = identifierAddBlock(finding, identifierWriteEnabled)
  const identifierName = t(`calibreAudit.identifierNames.${finding.evidenceKey}`, { defaultValue: finding.evidenceKey })
  const agreement = finding.field === 'identifiers' && finding.state === 'resolved'
  return <li className="space-y-3 border-t border-slate-200 dark:border-zinc-700 pt-3">
    <div className="flex flex-wrap items-start justify-between gap-2">
      <h4 className="border-l-2 border-slate-300 dark:border-zinc-600 pl-3 text-sm font-semibold">
        {finding.field === 'identifiers' ? t('calibreAudit.identifierComparison', { identifier: identifierName }) : t(`calibreAudit.types.${finding.findingType}`)}
      </h4>
      <div className="flex flex-wrap gap-2 text-xs">
        <span className="rounded bg-slate-200 dark:bg-zinc-800 px-2 py-1">{t(agreement ? 'calibreAudit.agreement' : `calibreAudit.types.${finding.findingType}`)}</span>
        <span className={`rounded px-2 py-1 ${priority === 'actionable' ? 'bg-rose-100 text-rose-900 dark:bg-rose-950 dark:text-rose-200' : 'bg-slate-200 dark:bg-zinc-800'}`}>{t(`calibreAudit.priority.${priority}`)}</span>
        {editionID && <span className="rounded bg-amber-100 text-amber-900 dark:bg-amber-950 dark:text-amber-300 px-2 py-1">{t('calibreAudit.editionIdentifier')}</span>}
        {workID && <span className="rounded bg-sky-100 text-sky-900 dark:bg-sky-950 dark:text-sky-300 px-2 py-1">{t('calibreAudit.workIdentifier')}</span>}
        {finding.assessment === 'ambiguous' && current && <span className="rounded bg-amber-100 text-amber-900 dark:bg-amber-950 dark:text-amber-300 px-2 py-1">{t('calibreAudit.ambiguous')}</span>}
      </div>
    </div>
    {editionID && current && finding.assessment === 'ambiguous' && <p className="text-xs text-amber-700 dark:text-amber-400">{t('calibreAudit.editionHint')}</p>}
    <div className="grid sm:grid-cols-2 gap-3 text-sm">
      <div><h5 className="font-medium mb-1">{historical ? t('calibreAudit.priorOwned') : t('calibreAudit.owned')}</h5><Evidence values={finding.calibreEvidence} identifierType={finding.field === 'identifiers' ? finding.evidenceKey : undefined} /></div>
      <div><h5 className="font-medium mb-1">{historical ? t('calibreAudit.priorExternal') : t('calibreAudit.external')}</h5><Evidence values={finding.binderyEvidence} identifierType={finding.field === 'identifiers' ? finding.evidenceKey : undefined} /></div>
    </div>
    {!historical && <p className="text-sm text-fg-muted">{t(agreement ? 'calibreAudit.findingExplanation.agreement' : 'calibreAudit.findingExplanation.' + finding.findingType, {
      defaultValue: t('calibreAudit.findingExplanation.other'), identifier: identifierName,
    })}
      {current && finding.assessment === 'ambiguous' && <> {t('calibreAudit.reviewCaveat')}</>}</p>}
    {current && finding.field === 'identifiers' && addBlock &&
      <p className="text-sm text-fg-muted">{t('calibreAudit.blocked.' + addBlock)}</p>}
    {!addBlock && <IdentifierAdd key={finding.comparisonFingerprint} finding={finding} busy={busy} onRefresh={onRefresh} onNotice={onNotice} />}
    {!historical && <details className="text-xs text-fg-muted"><summary className="cursor-pointer">{t('calibreAudit.technicalDetails')}</summary>
      <p>{t('calibreAudit.rawReason')}: {finding.reason.replaceAll('stored provider record', 'stored metadata record')}</p>
      <p>{t('calibreAudit.matchDetails', { method: finding.matchMethod, confidence: finding.matchConfidence })}</p>
      <p>{t('calibreAudit.assessmentDetails', { assessment: finding.assessment, state: finding.state })}</p>
    </details>}
    <IdentifierAttemptHistory findingId={finding.id} revision={revision} />
    {!!finding.decisions?.length && <details className="text-xs text-fg-muted"><summary className="cursor-pointer">{t('calibreAudit.decisionHistory')}</summary><ul className="mt-1 space-y-1">{finding.decisions.map((decision, i) => <li key={i}>{t(`calibreAudit.actions.${decision.action}`)} · {new Date(decision.createdAt).toLocaleString()}</li>)}</ul></details>}
    <div className="flex flex-wrap gap-3 text-sm">
      {current && <button disabled={busy} onClick={() => onIgnore(finding)} className="text-emerald-700 dark:text-emerald-400 underline disabled:opacity-50">{t('calibreAudit.ignore')}</button>}
      {finding.state === 'ignored' && <button disabled={busy} onClick={() => onReopen(finding)} className="text-emerald-700 dark:text-emerald-400 underline disabled:opacity-50">{t('calibreAudit.reopen')}</button>}
    </div>
  </li>
}

type BookFindingsProps = Omit<FindingProps, 'finding'> & {
  findings: CalibreAuditFinding[]
  cwaURL: string
  identityGeneration: number
  loadIdentity: (bookId: number) => Promise<CalibreIdentitySnapshot>
}

function BookFindings({ findings, cwaURL, identityGeneration, loadIdentity, ...controls }: BookFindingsProps) {
  const { t } = useTranslation()
  const book = findings[0]
  const historical = book.state === 'unmatched'
  const target = cwaBookURL(cwaURL, book.calibreId)
  return <li className={`rounded-lg border p-4 space-y-4 ${historical ? 'border-dashed border-slate-400 dark:border-zinc-600 opacity-80' : 'border-slate-300 dark:border-zinc-700'}`}>
    <header className="space-y-1">
      <h3 className="text-lg font-semibold leading-snug">{!historical && <span className="block text-xs font-medium text-fg-muted">{t('calibreAudit.matchedBook')}</span>} <Link className="text-emerald-700 dark:text-emerald-400 underline" to={`/book/${book.bookId}`}>{book.bookTitle || `#${book.bookId}`}</Link></h3>
      <p className="text-xs text-fg-muted">{t('calibreAudit.calibreId', { id: book.calibreId })} · {historical ? t('calibreAudit.unmatchedHint') : t('calibreAudit.bookMatch.' + book.matchConfidence, { defaultValue: t('calibreAudit.bookMatch.other') })}</p>
      {target && <a href={target} target="_blank" rel="noopener noreferrer" className="inline-block text-sm text-emerald-700 dark:text-emerald-400 underline">{t('calibreAudit.openCWA')}</a>}
    </header>
    {!historical && <IdentityContext key={`${book.bookId}:${book.calibreId}:${identityGeneration}`} finding={book} loadIdentity={loadIdentity} />}
    <ul className="space-y-3">{findings.map(finding => <Finding key={finding.id} finding={finding} {...controls} />)}</ul>
  </li>
}

function groupFindings(items: CalibreAuditFinding[]): CalibreAuditFinding[][] {
  const groups: CalibreAuditFinding[][] = []
  const currentBooks = new Map<string, CalibreAuditFinding[]>()
  for (const finding of items) {
    // Historical records are per-finding snapshots, never current book state.
    if (finding.state === 'unmatched') { groups.push([finding]); continue }
    const key = `${finding.bookId}:${finding.calibreId}`
    let group = currentBooks.get(key)
    if (!group) { group = []; currentBooks.set(key, group); groups.push(group) }
    group.push(finding)
  }
  return groups
}

export default function CalibreAuditPage() {
  const { t } = useTranslation()
  const [items, setItems] = useState<CalibreAuditFinding[]>([])
  // One snapshot per book on this result page, even when it has several findings.
  const identityRequests = useRef(new Map<number, Promise<CalibreIdentitySnapshot>>())
  const loadIdentity = useCallback((bookId: number) => {
    let request = identityRequests.current.get(bookId)
    if (!request) { request = api.calibreAuditIdentity(bookId); identityRequests.current.set(bookId, request) }
    return request
  }, [])
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
  const [identityGeneration, setIdentityGeneration] = useState(0)
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
      .then(data => { if (active) { identityRequests.current.clear(); setIdentityGeneration(n => n + 1); setItems(data.items); setTotal(data.total); setError('') } })
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
    {loading ? <p role="status">{t('common.loading')}</p> : error ? <p role="alert" className="text-red-600 dark:text-red-400">{error}</p> : items.length === 0 ? <p>{t('calibreAudit.empty')}</p> : <ul className="space-y-3">{groupFindings(items).map(group => <BookFindings key={group[0].state === 'unmatched' ? `historical:${group[0].id}` : `book:${group[0].bookId}:${group[0].calibreId}`} findings={group} cwaURL={cwaURL} busy={busy} identifierWriteEnabled={identifierWriteEnabled} revision={revision} identityGeneration={identityGeneration} loadIdentity={loadIdentity} onRefresh={refresh} onNotice={identifierNotice} onIgnore={ignore} onReopen={reopen} />)}</ul>}
    {!error && <Pagination {...paginationProps}
      onPageChange={next => { setLoading(true); paginationProps.onPageChange(next) }}
      onPageSizeChange={next => { setLoading(true); paginationProps.onPageSizeChange(next) }} />}
  </section>
}
