/**
 * Turns a heading's text into the id the reader gives that heading.
 *
 * This is the reference implementation of the anchor normalization
 * (docs/srs/zh-search-and-retrieval.srs.md FR-502); the server mirrors it in
 * markdown.NormalizeHeadingID and both sides are pinned by
 * headingAnchorVectors.json.
 */
export function slugifyHeadline(text: string) {
  return text
    .normalize('NFKD')
    .replace(/[ßẞ]/g, 'ss')
    .toLowerCase()
    .replace(/\p{Mark}+/gu, '')
    .trim()
    .replace(/[^\p{Letter}\p{Number}\s-]/gu, '')
    .replace(/[\s_-]+/g, '-')
    .replace(/^-+|-+$/g, '')
}
