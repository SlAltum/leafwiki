import { slugifyHeadline } from './headingAnchor'

/**
 * findHeadlineElement returns the heading the fragment points at: an exact id
 * first, then the normalized form of it.
 */
function findHeadlineElement(headlineId: string): HTMLElement | null {
  const exact = document.getElementById(headlineId)
  if (exact) return exact

  const normalized = slugifyHeadline(headlineId)
  if (!normalized || normalized === headlineId) return null

  return document.getElementById(normalized)
}

type ScrollToHeadlineOptions = {
  behavior?: ScrollBehavior
  waitForStableLayout?: boolean
}

export function scrollToHeadlineHash(
  hash: string,
  {
    behavior = 'smooth',
    waitForStableLayout = true,
  }: ScrollToHeadlineOptions = {},
): () => void {
  const contentContainer = document.getElementById(
    'scroll-container',
  ) as HTMLElement | null
  if (!contentContainer) return () => {}

  let cancelled = false

  function waitUntilHeightStabilizes(
    element: HTMLElement,
    callback: () => void,
    interval = 250,
    maxTotalTime = 3000,
    stableTime = 500,
  ) {
    let lastHeight = element.scrollHeight
    let stableFor = 0
    let elapsedTime = 0

    const checkHeight = () => {
      const currentHeight = element.scrollHeight
      if (currentHeight === lastHeight) {
        stableFor += interval
        if (stableFor >= stableTime) {
          callback()
          return
        }
      } else {
        lastHeight = currentHeight
        stableFor = 0
      }
      elapsedTime += interval
      if (elapsedTime < maxTotalTime) {
        setTimeout(checkHeight, interval)
      } else {
        callback()
      }
    }

    setTimeout(checkHeight, interval)
  }

  const scrollToTarget = () => {
    if (cancelled) return

    const rawHeadlineId = hash.substring(1)
    let headlineId = rawHeadlineId

    try {
      headlineId = decodeURIComponent(rawHeadlineId)
    } catch {
      headlineId = rawHeadlineId
    }

    // A hand-written fragment ("#Nika's clothes") and the id the reader
    // generated ("nikas-clothes") name the same section, so the normalized
    // form is tried as well (FR-502, FR-505).
    const headlineElement = findHeadlineElement(headlineId)
    if (!headlineElement) {
      console.warn(`Headline with id "${headlineId}" not found.`)
      return
    }
    history.replaceState(null, '', hash)
    headlineElement.scrollIntoView({ behavior, block: 'start' })
  }

  if (waitForStableLayout) {
    waitUntilHeightStabilizes(contentContainer, scrollToTarget)
  } else {
    scrollToTarget()
  }

  return () => {
    cancelled = true
  }
}
