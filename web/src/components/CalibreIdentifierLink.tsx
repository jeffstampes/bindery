import { auditIdentifierRecordLink } from '../util/metadataSource'

// Presentation only: a link never changes the authority or eligibility of an identifier.
export default function CalibreIdentifierLink({ type, value, provider }: {
  type: string; value: string; provider?: string
}) {
  const record = auditIdentifierRecordLink({ type, value, provider })
  return record
    ? <a href={record.url} target="_blank" rel="noopener noreferrer" className="text-emerald-700 dark:text-emerald-400 underline">{value} ↗</a>
    : <>{value}</>
}
