import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import Dashboard from './DashboardPage.jsx'

const response = (data) => ({ ok: true, json: async () => data })

function renderWithHealth(status) {
  vi.stubGlobal('fetch', vi.fn(async (url) => {
    if (url === '/healthz') return response({ status })
    if (url === '/api/hauler/capabilities') return response({ version: { Full: 'v1.3.2' } })
    throw new Error(`Unexpected request: ${url}`)
  }))
  render(<MemoryRouter><Dashboard /></MemoryRouter>)
}

describe('Dashboard', () => {
  afterEach(() => {
    cleanup()
    vi.unstubAllGlobals()
  })

  it('shows plain backend health instead of a job status badge', async () => {
    renderWithHealth('ok')
    expect(await screen.findByText('Backend OK')).toBeInTheDocument()
    expect(screen.queryByText('succeeded')).not.toBeInTheDocument()
  })

  it('names an unhealthy backend plainly', async () => {
    renderWithHealth('error')
    expect(await screen.findByText('Backend unreachable')).toBeInTheDocument()
  })

  it('points a new admin at creating a haul', async () => {
    renderWithHealth('ok')
    await screen.findByText('Backend OK')
    expect(screen.getByRole('link', { name: 'Create a haul' })).toHaveAttribute('href', '/hauls')
    expect(screen.getByRole('link', { name: 'Store Operations' })).toHaveAttribute('href', '/store')
  })
})
