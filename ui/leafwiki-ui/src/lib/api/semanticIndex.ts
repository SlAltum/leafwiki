import { ApiError, fetchWithAuth } from './auth'

export type SemanticIndexState =
  'off' | 'missing' | 'queued' | 'running' | 'stale' | 'failed' | 'fresh'

export interface SemanticIndexStatus {
  state: SemanticIndexState
  paused: boolean
  pending: number
  build_id?: string
  model_id?: string
  tokenizer_version?: string
  last_error?: string
  running_since?: string
  queued_since?: string
}

const STATUS_URL = '/api/admin/search/vectors/status'
const REBUILD_URL = '/api/admin/search/vectors/rebuild'
const PAUSE_URL = '/api/admin/search/vectors/pause'
const INDEX_URL = '/api/admin/search/vectors'

// The status endpoint answers 503 while the feature is switched off. That is a
// state of the page, not a failure of it.
export async function fetchSemanticIndexStatus(): Promise<SemanticIndexStatus> {
  try {
    return await fetchWithAuth<SemanticIndexStatus>(STATUS_URL, {})
  } catch (err) {
    if (err instanceof ApiError && err.status === 503) {
      return { state: 'off', paused: false, pending: 0 }
    }
    throw err
  }
}

export async function rebuildSemanticIndex(): Promise<void> {
  await fetchWithAuth(REBUILD_URL, { method: 'POST' })
}

export async function setSemanticIndexPaused(paused: boolean): Promise<void> {
  await fetchWithAuth(PAUSE_URL, {
    method: 'POST',
    body: JSON.stringify({ paused }),
  })
}

export async function clearSemanticIndex(): Promise<void> {
  await fetchWithAuth(INDEX_URL, { method: 'DELETE' })
}

export type SemanticPageState =
  'off' | 'indexed' | 'stale' | 'queued' | 'failed' | 'missing'

export interface SemanticPageStatus {
  page_id: string
  state: SemanticPageState
  passages: number
  last_error?: string
  queued_at?: string
}

// One page's vectorisation, separate from the whole index: a single page can be
// tracked and rebuilt without a full rebuild.
export async function fetchPageIndexStatus(
  pageId: string,
): Promise<SemanticPageStatus> {
  try {
    return await fetchWithAuth<SemanticPageStatus>(
      `/api/admin/search/vectors/pages/${encodeURIComponent(pageId)}`,
      {},
    )
  } catch (err) {
    if (err instanceof ApiError && err.status === 503) {
      return { page_id: pageId, state: 'off', passages: 0 }
    }
    throw err
  }
}

export async function rebuildPageIndex(pageId: string): Promise<void> {
  await fetchWithAuth(
    `/api/admin/search/vectors/pages/${encodeURIComponent(pageId)}/rebuild`,
    { method: 'POST' },
  )
}
