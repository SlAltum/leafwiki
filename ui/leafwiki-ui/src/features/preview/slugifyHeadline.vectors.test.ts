import { describe, expect, it } from 'vitest'
import vectors from './headingAnchorVectors.json'
import { slugifyHeadline } from './rehypeLineNumber'

// The same fixture is read by internal/core/markdown/headinganchor_test.go, so
// the Go and TypeScript implementations of the anchor normalization cannot
// drift apart (FR-502, FR-506).
describe('slugifyHeadline shared vectors', () => {
  it('has cases', () => {
    expect(vectors.cases.length).toBeGreaterThan(0)
  })

  for (const { input, want } of vectors.cases) {
    it(`normalizes ${JSON.stringify(input)}`, () => {
      expect(slugifyHeadline(input)).toBe(want)
    })
  }
})
