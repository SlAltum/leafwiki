import '@/lib/i18n'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ReactNode } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { PageSemanticIndexDialog } from './PageSemanticIndexDialog'

// The real BaseDialog is driven by the dialog store; the tests mount the body
// directly, the same way the other dialog tests do.
vi.mock('@/components/BaseDialog', () => ({
  default: ({ children }: { children?: ReactNode }) => <div>{children}</div>,
}))

vi.mock('sonner', () => ({
  toast: { error: vi.fn(), success: vi.fn() },
}))

vi.mock('@/lib/api/semanticIndex', () => ({
  fetchPageIndexStatus: vi.fn(),
  rebuildPageIndex: vi.fn(),
}))

import { toast } from 'sonner'
import { fetchPageIndexStatus, rebuildPageIndex } from '@/lib/api/semanticIndex'

const fetchStatus = vi.mocked(fetchPageIndexStatus)
const rebuild = vi.mocked(rebuildPageIndex)

const page = { id: 'page-1', title: 'Doc' }

beforeEach(() => {
  vi.clearAllMocks()
  rebuild.mockResolvedValue(undefined)
})

describe('PageSemanticIndexDialog', () => {
  it('shows an indexed page and offers a rebuild', async () => {
    fetchStatus.mockResolvedValue({
      page_id: 'page-1',
      state: 'indexed',
      passages: 4,
    })
    render(<PageSemanticIndexDialog page={page} />)

    expect(
      await screen.findByTestId('page-semantic-index-state'),
    ).toHaveTextContent(/indexed/i)
    expect(
      screen.getByTestId('page-semantic-index-passages'),
    ).toHaveTextContent('4')
    expect(screen.getByTestId('page-semantic-index-rebuild')).toBeEnabled()
  })

  it('rebuilding one page asks only that page', async () => {
    const user = userEvent.setup({ pointerEventsCheck: 0 })
    fetchStatus.mockResolvedValue({
      page_id: 'page-1',
      state: 'stale',
      passages: 2,
    })
    render(<PageSemanticIndexDialog page={page} />)

    await user.click(await screen.findByTestId('page-semantic-index-rebuild'))

    expect(rebuild).toHaveBeenCalledWith('page-1')
    expect(toast.success).toHaveBeenCalled()
  })

  it('a disabled server explains itself and disables the rebuild', async () => {
    fetchStatus.mockResolvedValue({
      page_id: 'page-1',
      state: 'off',
      passages: 0,
    })
    render(<PageSemanticIndexDialog page={page} />)

    expect(
      await screen.findByTestId('page-semantic-index-state'),
    ).toHaveTextContent(/disabled/i)
    expect(screen.getByTestId('page-semantic-index-rebuild')).toBeDisabled()
  })

  it('a failed page surfaces the recorded error', async () => {
    fetchStatus.mockResolvedValue({
      page_id: 'page-1',
      state: 'failed',
      passages: 0,
      last_error: 'encoder exploded',
    })
    render(<PageSemanticIndexDialog page={page} />)

    expect(await screen.findByText(/encoder exploded/)).toBeInTheDocument()
  })
})
