import { describe, expect, it } from 'vitest'
import {
  hasMinQueryLength,
  minQueryLength,
  MIN_QUERY_LENGTH_ASCII,
  MIN_QUERY_LENGTH_CJK,
} from './searchQuery'

describe('minQueryLength', () => {
  it('needs three characters for a Latin query', () => {
    expect(minQueryLength('deploy')).toBe(MIN_QUERY_LENGTH_ASCII)
  })

  it('needs two for a Han query', () => {
    expect(minQueryLength('部署')).toBe(MIN_QUERY_LENGTH_CJK)
  })

  it('needs two for a kana query', () => {
    expect(minQueryLength('カタ')).toBe(MIN_QUERY_LENGTH_CJK)
  })

  it('treats a mixed query as CJK', () => {
    expect(minQueryLength('deploy 部署')).toBe(MIN_QUERY_LENGTH_CJK)
  })
})

describe('hasMinQueryLength', () => {
  it('accepts a two-character Chinese word', () => {
    expect(hasMinQueryLength('部署')).toBe(true)
  })

  it('rejects a single Chinese character', () => {
    expect(hasMinQueryLength('枪')).toBe(false)
  })

  it('accepts three Latin characters', () => {
    expect(hasMinQueryLength('abc')).toBe(true)
  })

  it('rejects two Latin characters', () => {
    expect(hasMinQueryLength('ab')).toBe(false)
  })

  it('ignores surrounding whitespace', () => {
    expect(hasMinQueryLength('  部署  ')).toBe(true)
    expect(hasMinQueryLength('  ab  ')).toBe(false)
    expect(hasMinQueryLength('   ')).toBe(false)
  })

  it('counts characters, not UTF-16 units', () => {
    // Two astral-plane CJK characters are four UTF-16 units.
    expect(hasMinQueryLength('\u{20000}\u{20001}')).toBe(true)
  })
})
