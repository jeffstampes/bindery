// Build a link to the upstream metadata source for an author or book, à la
// the *arr stacks' TMDB/IMDB links (#1296). The provider is implied by the
// foreign-ID prefix, matching the backend convention in
// internal/metadata/aggregator_providers.go and models.AuthorProviderFromForeignID.
//
// We only emit a link when the public URL can be constructed reliably from the
// stored ID. Calibre and Audiobookshelf IDs are local to those systems, so they
// return null rather than risk a dead link.

export type MetadataSourceLink = { url: string; label: string }

// Navigation only: an explicit identifier type (or the provider of a foreign
// record) selects a known direct-record route. Keep validation stricter than
// metadataSourceLink, which also serves older, less structured catalogue IDs.
export function auditIdentifierRecordLink({ type, value, provider }: {
  type: string; value: string; provider?: string
}): MetadataSourceLink | null {
  const kind = (type === 'foreign_id' ? provider : type)?.toLowerCase()
  const raw = value.trim()
  if (!raw) return null

  if (['openlibrary', 'ol', 'openlibrary_work', 'openlibrary_edition', 'openlibrary_author'].includes(kind ?? '')) {
    const id = raw.replace(/^openlibrary:/i, '')
    const suffix = kind === 'openlibrary_edition' ? 'M' : kind === 'openlibrary_author' ? 'A'
      : kind === 'openlibrary_work' ? 'W' : '[WMA]'
    if (!new RegExp(`^OL\\d+${suffix}$`).test(id)) return null
    return metadataSourceLink(id, id.endsWith('A') ? 'author' : 'book')
  }
  if (kind === 'google' || kind === 'googlebooks') {
    const id = raw.replace(/^gb:/i, '')
    return /^[A-Za-z0-9_-]+$/.test(id) ? metadataSourceLink(`gb:${id}`, 'book') : null
  }
  if (kind === 'hardcover') {
    const id = raw.replace(/^hc:/i, '')
    // A bare OpenLibrary key is not a known Hardcover slug; numeric values
    // may be database IDs rather than slugs and are not direct-record links.
    if (/^OL\d+[WMA]$/.test(raw) || /^\d+$/.test(id)) return null
    return /^[a-z0-9][a-z0-9-]*$/i.test(id) ? metadataSourceLink(`hc:${id}`, 'book') : null
  }
  if (kind === 'dnb') {
    const id = raw.replace(/^dnb:/i, '')
    return /^\d+$/.test(id) ? metadataSourceLink(`dnb:${id}`, 'book') : null
  }
  if (kind === 'isbn') {
    const id = raw.toUpperCase().replace(/[\s-]/g, '')
    if (/^\d{9}[\dX]$/.test(id)) {
      const sum = [...id].reduce((n, digit, i) => n + (digit === 'X' ? 10 : Number(digit)) * (10 - i), 0)
      if (sum % 11 === 0) return { url: `https://openlibrary.org/isbn/${id}`, label: 'OpenLibrary' }
    }
    if (/^97[89]\d{10}$/.test(id)) {
      const sum = [...id].reduce((n, digit, i) => n + Number(digit) * (i % 2 ? 3 : 1), 0)
      if (sum % 10 === 0) return { url: `https://openlibrary.org/isbn/${id}`, label: 'OpenLibrary' }
    }
    return null
  }
  if (kind === 'asin') {
    const id = raw.toUpperCase()
    return /^[A-Z0-9]{10}$/.test(id) ? { url: `https://www.amazon.com/dp/${id}`, label: 'Amazon' } : null
  }
  return null
}

export function metadataSourceLink(
  foreignId: string | undefined | null,
  kind: 'author' | 'book',
): MetadataSourceLink | null {
  const id = (foreignId ?? '').trim()
  if (!id) return null
  const lowerID = id.toLowerCase()

  if (lowerID.startsWith('gb:')) {
    const vol = id.slice(3).trim()
    // Google Books has no canonical author page; only books map cleanly.
    if (kind !== 'book' || !vol) return null
    return { url: `https://books.google.com/books?id=${encodeURIComponent(vol)}`, label: 'Google Books' }
  }

  if (kind === 'book' && lowerID.startsWith('hc:')) {
    const value = id.slice(3).trim()
    // Numeric values are ambiguous: Hardcover permits numeric slugs and also
    // uses numeric database ids as a fallback when a book has no slug.
    if (!/^[a-z0-9][a-z0-9-]*$/i.test(value) || /^\d+$/.test(value)) return null
    return { url: `https://hardcover.app/books/${encodeURIComponent(value)}`, label: 'Hardcover' }
  }

  if (kind === 'book' && lowerID.startsWith('dnb:')) {
    const controlNumber = id.slice(4).trim()
    if (!/^\d+$/.test(controlNumber)) return null
    return { url: `https://d-nb.info/${controlNumber}`, label: 'DNB' }
  }

  // No reliable public URL for these providers.
  if (lowerID.startsWith('hc:') || lowerID.startsWith('dnb:') || lowerID.startsWith('abs:') || lowerID.startsWith('calibre:')) {
    return null
  }

  // Default: OpenLibrary, whose foreign IDs are bare OL keys. Author keys end
  // in A, work keys in W, edition keys in M.
  if (kind === 'author') {
    if (!/^OL\w+A$/i.test(id)) return null
    return { url: `https://openlibrary.org/authors/${id}`, label: 'OpenLibrary' }
  }
  if (/^OL\w+W$/i.test(id)) return { url: `https://openlibrary.org/works/${id}`, label: 'OpenLibrary' }
  if (/^OL\w+M$/i.test(id)) return { url: `https://openlibrary.org/books/${id}`, label: 'OpenLibrary' }
  return null
}

// Human-readable name for a metadata provider key as stored in
// books.metadata_provider / book_identifiers.provider (#1707). Unknown keys are
// returned as-is so a provider added on the backend still shows something
// truthful instead of being hidden.
const PROVIDER_NAMES: Record<string, string> = {
  openlibrary: 'OpenLibrary',
  hardcover: 'Hardcover',
  googlebooks: 'Google Books',
  dnb: 'DNB',
  calibre: 'Calibre',
  audiobookshelf: 'Audiobookshelf',
}

export function providerDisplayName(provider: string | undefined | null): string {
  const key = (provider ?? '').trim().toLowerCase()
  if (!key) return ''
  return PROVIDER_NAMES[key] ?? provider!.trim()
}

// Which provider a book foreign ID belongs to, mirroring
// models.BookProviderFromForeignID so the page can name the source even for
// rows whose metadata_provider column was never written. An unprefixed ID is
// OpenLibrary, matching the long-standing books.foreign_id convention.
export function providerFromBookForeignId(foreignId: string | undefined | null): string {
  const id = (foreignId ?? '').trim().toLowerCase()
  if (id.startsWith('gb:')) return 'googlebooks'
  if (id.startsWith('hc:')) return 'hardcover'
  if (id.startsWith('dnb:')) return 'dnb'
  if (id.startsWith('calibre:')) return 'calibre'
  if (id.startsWith('abs:')) return 'audiobookshelf'
  return 'openlibrary'
}

// Public page for a Hardcover series (#1708).
//
// hardcover.app routes series on their slug, not on the numeric id Bindery
// stores as hardcoverProviderId, so there is nothing to build a link from until
// the slug is known. Returns null in that case and the caller renders plain
// text, which is the same "no reliable public URL, no link" rule
// metadataSourceLink follows.
export function hardcoverSeriesUrl(slug: string | undefined | null): string | null {
  const value = (slug ?? '').trim()
  if (!value) return null
  return `https://hardcover.app/series/${encodeURIComponent(value)}`
}
