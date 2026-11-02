/**
 * A two-character CJK query is already a word ("部署"), while a Latin one needs
 * three characters to be worth a request. One predicate keeps the input box,
 * the URL effect, the scroll behaviour and the API call from disagreeing.
 */
export const MIN_QUERY_LENGTH_ASCII = 3
export const MIN_QUERY_LENGTH_CJK = 2

const CJK_PATTERN =
  /[\p{Script=Han}\p{Script=Hiragana}\p{Script=Katakana}\p{Script=Hangul}]/u

/** minQueryLength returns how many characters query needs to be searched. */
export function minQueryLength(query: string): number {
  return CJK_PATTERN.test(query) ? MIN_QUERY_LENGTH_CJK : MIN_QUERY_LENGTH_ASCII
}

/** hasMinQueryLength reports whether the trimmed query is long enough to search. */
export function hasMinQueryLength(query: string): boolean {
  const trimmed = query.trim()
  return [...trimmed].length >= minQueryLength(trimmed)
}
