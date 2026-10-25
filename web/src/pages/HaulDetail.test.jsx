import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import HaulDetail from './HaulDetail.jsx'

const haul = { id: 1, name: 'Demo', slug: 'demo', storeDir: '/data/hauls/demo/store' }

vi.mock('../contexts/useHauls.js', () => ({
  useHauls: () => ({ hauls: [haul], activeHaulId: 1, setActiveHaulId: () => {}, refreshHauls: () => {} }),
}))
vi.mock('../contexts/useJobs.js', () => ({
  useJobs: () => ({ fetchJobs: () => {} }),
}))

function renderHaul() {
  render(
    <MemoryRouter initialEntries={['/hauls/1']}>
      <Routes>
        <Route path="/hauls/:id" element={<HaulDetail />} />
      </Routes>
    </MemoryRouter>,
  )
}

describe('Haul detail page', () => {
  afterEach(() => {
    cleanup()
    vi.unstubAllGlobals()
  })

  it('names the publish tab Client Access, distinct from the Publishing page', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => ({ ok: true, json: async () => ({ routes: [], registryPort: 5000 }) })))
    renderHaul()
    // No tab or quick action is labelled "Publish"; that name belongs to the Publishing page.
    expect(screen.queryByRole('button', { name: /^Publish$/ })).not.toBeInTheDocument()
    expect(screen.getAllByRole('button', { name: 'Client Access' })).toHaveLength(2)

    fireEvent.click(screen.getAllByRole('button', { name: 'Client Access' })[0])
    expect(screen.getByText('Publish this Haul')).toBeInTheDocument()
    expect(await screen.findByRole('button', { name: 'Publish' })).toBeInTheDocument()
  })
})
