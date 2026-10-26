import { afterEach, describe, expect, it, vi } from 'vitest'
import { act, fireEvent, render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'

vi.mock('../contexts/useHauls.js', () => ({
  useHauls: () => ({ activeHaul: { id: 1, name: 'first' } }),
}))
vi.mock('../contexts/useJobs.js', () => ({
  useJobs: () => ({ fetchJobs: async () => {} }),
}))

import StoreSync from './StoreSync.jsx'

describe('StoreSync', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  // hauler runs a product's manifest from the product registry without Wagon
  // checking it, so the API refuses both options and the page never sends them.
  it('has no products or product registry inputs and never sends them', async () => {
    const fetchMock = vi.fn(async (url, opts) => {
      if (opts?.method === 'POST') return { ok: true, json: async () => ({ jobId: 9 }) }
      return { ok: true, json: async () => [] }
    })
    vi.stubGlobal('fetch', fetchMock)
    const { container } = render(
      <MemoryRouter>
        <StoreSync />
      </MemoryRouter>,
    )
    await act(async () => {})
    fireEvent.click(screen.getByRole('button', { name: /show advanced/i }))
    expect(screen.queryByText(/products/i)).toBeNull()
    expect(screen.queryByText(/product registry/i)).toBeNull()
    expect(container.querySelector('input[placeholder*="rgcrprod"]')).toBeNull()

    fireEvent.click(screen.getByRole('button', { name: /paste yaml/i }))
    fireEvent.change(container.querySelector('textarea'), { target: { value: 'kind: Files' } })
    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: /start sync/i }))
    })
    const posts = fetchMock.mock.calls.filter(([, opts]) => opts?.method === 'POST')
    expect(posts).toHaveLength(1)
    expect(posts[0][0]).toBe('/api/store/sync')
    const body = JSON.parse(posts[0][1].body)
    expect(body).not.toHaveProperty('products')
    expect(body).not.toHaveProperty('productRegistry')
  })
})
