import { useProgressbarStore } from '@/features/progressbar/progressbarStore'
import { render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter, Route, Routes, useLocation } from 'react-router'
import { afterEach, describe, expect, it, vi } from 'vitest'
import PermalinkRedirect from './PermalinkRedirect'

vi.mock('@/lib/api/pages', async () => {
  const actual =
    await vi.importActual<typeof import('@/lib/api/pages')>('@/lib/api/pages')
  return {
    ...actual,
    getPermalinkTarget: vi.fn(),
  }
})

vi.mock('react-i18next', () => ({
  initReactI18next: {
    type: '3rdParty',
    init: () => {},
  },
  useTranslation: () => ({ t: (key: string) => key }),
}))

import { getPermalinkTarget } from '@/lib/api/pages'
const mockGetPermalinkTarget = getPermalinkTarget as ReturnType<typeof vi.fn>

afterEach(() => {
  mockGetPermalinkTarget.mockReset()
  useProgressbarStore.setState({ loading: false })
})

function Landing() {
  const location = useLocation()
  return (
    <div data-testid="landing">
      {location.pathname + location.search + location.hash}
    </div>
  )
}

function renderAt(entry: string) {
  return render(
    <MemoryRouter initialEntries={[entry]}>
      <Routes>
        <Route path="/p/:id/:slug?" element={<PermalinkRedirect />} />
        <Route path="/:page" element={<Landing />} />
      </Routes>
    </MemoryRouter>,
  )
}

describe('PermalinkRedirect', () => {
  it('keeps the fragment of the incoming permalink', async () => {
    // A permalink to a section has to land on that section (FR-503).
    mockGetPermalinkTarget.mockResolvedValue({ path: 'target' })

    renderAt('/p/page-1#rollback')

    await waitFor(() =>
      expect(screen.getByTestId('landing')).toHaveTextContent(
        '/target#rollback',
      ),
    )
  })

  it('redirects to the page without a fragment', async () => {
    mockGetPermalinkTarget.mockResolvedValue({ path: 'target' })

    renderAt('/p/page-1')

    await waitFor(() =>
      expect(screen.getByTestId('landing')).toHaveTextContent('/target'),
    )
  })
})
