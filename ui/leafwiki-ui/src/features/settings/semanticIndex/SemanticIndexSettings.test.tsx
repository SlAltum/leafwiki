import '@/lib/i18n'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import SemanticIndexSettings from './SemanticIndexSettings'

vi.mock('sonner', () => ({
  toast: { error: vi.fn(), success: vi.fn() },
}))

vi.mock('@/lib/api/semanticIndex', () => ({
  fetchSemanticIndexStatus: vi.fn(),
  rebuildSemanticIndex: vi.fn(),
  setSemanticIndexPaused: vi.fn(),
  clearSemanticIndex: vi.fn(),
}))

import { toast } from 'sonner'
import {
  clearSemanticIndex,
  fetchSemanticIndexStatus,
  rebuildSemanticIndex,
  setSemanticIndexPaused,
} from '@/lib/api/semanticIndex'

const fetchStatus = vi.mocked(fetchSemanticIndexStatus)
const rebuild = vi.mocked(rebuildSemanticIndex)
const setPaused = vi.mocked(setSemanticIndexPaused)
const clear = vi.mocked(clearSemanticIndex)

beforeEach(() => {
  vi.clearAllMocks()
  rebuild.mockResolvedValue(undefined)
  setPaused.mockResolvedValue(undefined)
  clear.mockResolvedValue(undefined)
})

describe('SemanticIndexSettings', () => {
  it('disabled: explains how to switch it on and offers no actions', async () => {
    fetchStatus.mockResolvedValue({ state: 'off', paused: false, pending: 0 })
    render(<SemanticIndexSettings />)

    expect(
      await screen.findByTestId('semantic-index-disabled'),
    ).toBeInTheDocument()
    expect(screen.getByTestId('semantic-index-rebuild')).toBeDisabled()
    expect(screen.getByTestId('semantic-index-auto-checkbox')).toBeDisabled()
  })

  it('missing: shows the state and hint, rebuild stays available', async () => {
    fetchStatus.mockResolvedValue({
      state: 'missing',
      paused: false,
      pending: 0,
    })
    render(<SemanticIndexSettings />)

    expect(await screen.findByTestId('semantic-index-state')).toHaveTextContent(
      /not built yet/i,
    )
    expect(screen.getByTestId('semantic-index-rebuild')).toBeEnabled()
  })

  it('rebuild asks the API and reports success', async () => {
    const user = userEvent.setup({ pointerEventsCheck: 0 })
    fetchStatus.mockResolvedValue({ state: 'fresh', paused: false, pending: 0 })
    render(<SemanticIndexSettings />)

    await user.click(await screen.findByTestId('semantic-index-rebuild'))

    expect(rebuild).toHaveBeenCalledTimes(1)
    expect(toast.success).toHaveBeenCalled()
  })

  it('pausing the automatic index writes the flipped flag', async () => {
    const user = userEvent.setup({ pointerEventsCheck: 0 })
    fetchStatus.mockResolvedValue({ state: 'fresh', paused: false, pending: 0 })
    render(<SemanticIndexSettings />)

    await user.click(await screen.findByTestId('semantic-index-auto-checkbox'))

    expect(setPaused).toHaveBeenCalledWith(true)
    expect(toast.success).toHaveBeenCalled()
  })

  it('clearing requires the confirm dialog', async () => {
    const user = userEvent.setup({ pointerEventsCheck: 0 })
    fetchStatus.mockResolvedValue({ state: 'fresh', paused: false, pending: 0 })
    render(<SemanticIndexSettings />)

    await user.click(await screen.findByTestId('semantic-index-clear'))
    expect(clear).not.toHaveBeenCalled()

    await user.click(
      await screen.findByRole('button', { name: /clear and rebuild/i }),
    )
    await waitFor(() => expect(clear).toHaveBeenCalledTimes(1))
  })

  it('surfaces a status load failure', async () => {
    fetchStatus.mockRejectedValueOnce(new Error('boom'))
    render(<SemanticIndexSettings />)

    expect(
      await screen.findByTestId('semantic-index-status-error'),
    ).toBeInTheDocument()
  })
})
