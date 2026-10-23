import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, act } from '@testing-library/react'

// The page reads the active haul from HaulContext; drive it directly.
let mockActiveHaul = { id: 1, name: 'first' }
vi.mock('../contexts/HaulContext.jsx', () => ({
  useHauls: () => ({ activeHaul: mockActiveHaul }),
}))

import Serve from './ServePage.jsx'

const serveCalls = (fetchMock, kind) =>
  fetchMock.mock.calls
    .map(([url]) => String(url))
    .filter((url) => url.startsWith(`/api/serve/${kind}`))

describe('ServePage active haul', () => {
  let fetchMock

  beforeEach(() => {
    vi.useFakeTimers()
    mockActiveHaul = { id: 1, name: 'first' }
    fetchMock = vi.fn(async () => ({ ok: true, json: async () => [] }))
    vi.stubGlobal('fetch', fetchMock)
  })

  afterEach(() => {
    vi.useRealTimers()
    vi.unstubAllGlobals()
  })

  it('fetches with the new ?haul= id after switching hauls, including on the next poll', async () => {
    const { rerender } = render(<Serve />)
    await act(async () => {})
    expect(serveCalls(fetchMock, 'registry')).toEqual(['/api/serve/registry?haul=1'])
    expect(serveCalls(fetchMock, 'fileserver')).toEqual(['/api/serve/fileserver?haul=1'])

    fetchMock.mockClear()
    mockActiveHaul = { id: 2, name: 'second' }
    rerender(<Serve />)
    await act(async () => {})

    // Switching re-fetches immediately for the new haul.
    expect(serveCalls(fetchMock, 'registry')).toEqual(['/api/serve/registry?haul=2'])
    expect(serveCalls(fetchMock, 'fileserver')).toEqual(['/api/serve/fileserver?haul=2'])

    // The polling interval must not keep using the previously active haul.
    fetchMock.mockClear()
    await act(async () => {
      await vi.advanceTimersByTimeAsync(5000)
    })
    expect(serveCalls(fetchMock, 'registry')).toEqual(['/api/serve/registry?haul=2'])
    expect(serveCalls(fetchMock, 'fileserver')).toEqual(['/api/serve/fileserver?haul=2'])
  })
})
