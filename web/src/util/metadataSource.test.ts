import { describe, it, expect } from 'vitest'
import {
  auditIdentifierRecordLink,
  hardcoverSeriesUrl,
  metadataSourceLink,
  providerDisplayName,
  providerFromBookForeignId,
} from './metadataSource'

describe('metadataSourceLink', () => {
  it('links OpenLibrary authors', () => {
    expect(metadataSourceLink('OL23919A', 'author')).toEqual({
      url: 'https://openlibrary.org/authors/OL23919A',
      label: 'OpenLibrary',
    })
  })

  it('links OpenLibrary works and editions for books', () => {
    expect(metadataSourceLink('OL27448W', 'book')?.url).toBe('https://openlibrary.org/works/OL27448W')
    expect(metadataSourceLink('OL7353617M', 'book')?.url).toBe('https://openlibrary.org/books/OL7353617M')
  })

  it('links Google Books only for books', () => {
    expect(metadataSourceLink('gb:zyTCAlFPjgYC', 'book')).toEqual({
      url: 'https://books.google.com/books?id=zyTCAlFPjgYC',
      label: 'Google Books',
    })
    expect(metadataSourceLink('gb:zyTCAlFPjgYC', 'author')).toBeNull()
  })

  it('links unambiguous Hardcover book slugs', () => {
    expect(metadataSourceLink('hc:project-hail-mary', 'book')).toEqual({
      url: 'https://hardcover.app/books/project-hail-mary',
      label: 'Hardcover',
    })
    expect(metadataSourceLink('HC:project-hail-mary', 'book')).toEqual({
      url: 'https://hardcover.app/books/project-hail-mary',
      label: 'Hardcover',
    })
  })

  it('hides ambiguous numeric Hardcover identifiers', () => {
    expect(metadataSourceLink('hc:1984', 'book')).toBeNull()
    expect(metadataSourceLink('hc:12345', 'book')).toBeNull()
  })

  it('links DNB book control numbers', () => {
    expect(metadataSourceLink('dnb:123456789', 'book')).toEqual({
      url: 'https://d-nb.info/123456789',
      label: 'DNB',
    })
    expect(metadataSourceLink('DNB:123456789', 'book')).toEqual({
      url: 'https://d-nb.info/123456789',
      label: 'DNB',
    })
  })

  it('returns null for local or malformed provider ids', () => {
    expect(metadataSourceLink('abs:abc', 'book')).toBeNull()
    expect(metadataSourceLink('calibre:7', 'book')).toBeNull()
    expect(metadataSourceLink('hc:bad/value', 'book')).toBeNull()
    expect(metadataSourceLink('dnb:gnd:118585665', 'book')).toBeNull()
    expect(metadataSourceLink('hc:project-hail-mary', 'author')).toBeNull()
  })

  it('returns null for empty / malformed ids', () => {
    expect(metadataSourceLink('', 'author')).toBeNull()
    expect(metadataSourceLink(undefined, 'book')).toBeNull()
    expect(metadataSourceLink('gb:', 'book')).toBeNull()
    expect(metadataSourceLink('OL123', 'author')).toBeNull() // no trailing A
  })
})

describe('auditIdentifierRecordLink', () => {
  it.each([
    ['openlibrary', 'OL27448W', 'https://openlibrary.org/works/OL27448W'],
    ['openlibrary_edition', 'OL7353617M', 'https://openlibrary.org/books/OL7353617M'],
    ['openlibrary_author', 'OL23919A', 'https://openlibrary.org/authors/OL23919A'],
    ['hardcover', 'hc:project-hail-mary', 'https://hardcover.app/books/project-hail-mary'],
    ['googlebooks', 'gb:zyTCAlFPjgYC', 'https://books.google.com/books?id=zyTCAlFPjgYC'],
    ['dnb', 'dnb:123456789', 'https://d-nb.info/123456789'],
    ['isbn', '978-0-306-40615-7', 'https://openlibrary.org/isbn/9780306406157'],
    ['isbn', '0-306-40615-2', 'https://openlibrary.org/isbn/0306406152'],
    ['asin', 'b000123456', 'https://www.amazon.com/dp/B000123456'],
  ])('resolves %s %s to its direct record', (type, value, url) => {
    expect(auditIdentifierRecordLink({ type, value })?.url).toBe(url)
  })

  it('uses the same normalized destination for bare and prefixed provider IDs', () => {
    expect(auditIdentifierRecordLink({ type: 'google', value: 'vol_1' })).toEqual(
      auditIdentifierRecordLink({ type: 'googlebooks', value: 'gb:vol_1' }))
    expect(auditIdentifierRecordLink({ type: 'hardcover', value: 'known-slug' })).toEqual(
      auditIdentifierRecordLink({ type: 'hardcover', value: 'hc:known-slug' }))
  })

  it.each([
    ['other', 'OL27448W'], ['goodreads', '12345'], ['openlibrary_work', 'OL7353617M'],
    ['openlibrary_edition', 'OL27448W'], ['openlibrary_author', 'OL27448W'],
    ['openlibrary', 'OL42W<script>'], ['google', 'javascript:alert(1)'],
    ['hardcover', 'hc:123'], ['hardcover', '123'], ['hardcover', 'hc:bad/value'], ['dnb', 'dnb:gnd:123'],
    ['isbn', '9780306406158'], ['isbn', 'not-an-isbn'], ['asin', 'B00/123456'],
  ])('leaves unsupported, ambiguous or malformed %s %s unlinked', (type, value) => {
    expect(auditIdentifierRecordLink({ type, value })).toBeNull()
  })

  it('does not let a mismatched provider turn an opaque value into a link', () => {
    expect(auditIdentifierRecordLink({ type: 'foreign_id', provider: 'hardcover', value: 'OL27448W' })).toBeNull()
    expect(auditIdentifierRecordLink({ type: 'foreign_id', value: 'OL27448W' })).toBeNull()
  })
})

describe('providerDisplayName', () => {
  it('names the providers a book can come from', () => {
    expect(providerDisplayName('openlibrary')).toBe('OpenLibrary')
    expect(providerDisplayName('hardcover')).toBe('Hardcover')
    expect(providerDisplayName('googlebooks')).toBe('Google Books')
    expect(providerDisplayName('dnb')).toBe('DNB')
    expect(providerDisplayName('calibre')).toBe('Calibre')
    expect(providerDisplayName('audiobookshelf')).toBe('Audiobookshelf')
  })

  it('is case insensitive and trims', () => {
    expect(providerDisplayName('  HardCover ')).toBe('Hardcover')
  })

  it('passes an unknown provider through rather than hiding it', () => {
    expect(providerDisplayName('somethingnew')).toBe('somethingnew')
  })

  it('returns empty for no provider', () => {
    expect(providerDisplayName('')).toBe('')
    expect(providerDisplayName(undefined)).toBe('')
    expect(providerDisplayName(null)).toBe('')
  })
})

describe('providerFromBookForeignId', () => {
  it('mirrors models.BookProviderFromForeignID', () => {
    expect(providerFromBookForeignId('gb:xyz')).toBe('googlebooks')
    expect(providerFromBookForeignId('hc:123')).toBe('hardcover')
    expect(providerFromBookForeignId('HC:123')).toBe('hardcover')
    expect(providerFromBookForeignId('dnb:123')).toBe('dnb')
    expect(providerFromBookForeignId('calibre:7')).toBe('calibre')
    expect(providerFromBookForeignId('abs:lib:item')).toBe('audiobookshelf')
  })

  it('treats an unprefixed id as OpenLibrary', () => {
    expect(providerFromBookForeignId('OL27448W')).toBe('openlibrary')
    expect(providerFromBookForeignId('something-random')).toBe('openlibrary')
    expect(providerFromBookForeignId('')).toBe('openlibrary')
  })
})

describe('hardcoverSeriesUrl', () => {
  it('builds the public series page from the slug', () => {
    expect(hardcoverSeriesUrl('the-stormlight-archive')).toBe(
      'https://hardcover.app/series/the-stormlight-archive',
    )
  })

  it('escapes anything that is not URL safe', () => {
    expect(hardcoverSeriesUrl('a b/c')).toBe('https://hardcover.app/series/a%20b%2Fc')
  })

  // hardcover.app does not route series on the numeric provider id, so a row
  // without a slug gets no link at all rather than one that 404s (#1708).
  it('returns null without a slug', () => {
    expect(hardcoverSeriesUrl('')).toBeNull()
    expect(hardcoverSeriesUrl('   ')).toBeNull()
    expect(hardcoverSeriesUrl(undefined)).toBeNull()
    expect(hardcoverSeriesUrl(null)).toBeNull()
  })
})
