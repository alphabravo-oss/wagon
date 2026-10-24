import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, act, screen, fireEvent } from '@testing-library/react'

// The page reads the active haul from HaulContext; drive it directly.
let mockActiveHaul = { id: 1, name: 'first' }
vi.mock('../contexts/useHauls.js', () => ({
  useHauls: () => ({ activeHaul: mockActiveHaul }),
}))

import { MemoryRouter } from 'react-router-dom'
import ServePage from './ServePage.jsx'

const Serve = () => (
  <MemoryRouter>
    <ServePage />
  </MemoryRouter>
)

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

describe('ServePage serve status', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('defaults the registry to 5002 and the fileserver to 5001', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => ({ ok: true, json: async () => [] })))
    render(<Serve />)
    await act(async () => {})
    const ports = screen.getAllByRole('spinbutton').slice(0, 1)
    expect(ports[0].value).toBe('5002')
    expect(screen.getByText(/default: 5002/)).toBeTruthy()
    expect(screen.getByText(/default: 5001/)).toBeTruthy()
    expect(screen.queryByText(/default: 8080/)).toBeNull()
  })

  it('shows the plain-text error when a serve fails to start', async () => {
    const message = 'Registry serve on port 5000 failed: exit status 1: ERR listen tcp :5000: bind: address already in use'
    vi.stubGlobal('fetch', vi.fn(async (url, opts) => {
      if (opts?.method === 'POST') {
        return { ok: false, text: async () => message + '\n' }
      }
      return { ok: true, json: async () => [] }
    }))
    render(<Serve />)
    await act(async () => {})
    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: /start registry/i }))
    })
    expect(screen.getByText(message)).toBeTruthy()
  })

  it('shows why a crashed serve exited', async () => {
    const crashed = { id: 7, pid: 4321, port: 5000, status: 'crashed', exitReason: 'exit status 1: bind: address already in use', args: {} }
    vi.stubGlobal('fetch', vi.fn(async (url) => ({
      ok: true,
      json: async () => (String(url).startsWith('/api/serve/registry') ? [crashed] : []),
    })))
    render(<Serve />)
    await act(async () => {})
    expect(screen.getByText('crashed')).toBeTruthy()
    expect(screen.getByText('exit status 1: bind: address already in use')).toBeTruthy()
    expect(screen.queryByRole('button', { name: 'Stop' })).toBeNull()
  })
})
