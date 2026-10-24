import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import Store from './StorePage.jsx'

vi.mock('../contexts/useHauls.js', () => ({
  useHauls: () => ({ activeHaul: { id: 1, name: 'Default', storeDir: '/data/hauls/default/store' } }),
}))

describe('Store Operations page', () => {
  afterEach(() => {
    cleanup()
    vi.unstubAllGlobals()
  })

  it('uses the sidebar name, real operation names, and one Serve entry point', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => ({ ok: true, json: async () => ({ haulerDir: '/data' }) })))
    render(<MemoryRouter><Store /></MemoryRouter>)
    expect(screen.getByRole('heading', { name: 'Store Operations' })).toBeInTheDocument()
    expect(screen.getByText('Sync from Manifests')).toBeInTheDocument()
    expect(screen.getByText('Save Archive')).toBeInTheDocument()
    // Serve lives in the sidebar only; it is not a store operation.
    expect(screen.queryByRole('link', { name: /Serve registry or fileserver/ })).not.toBeInTheDocument()
    // Related pages keep name and description as separate, readable parts.
    const hauls = screen.getByRole('link', { name: /^Hauls/ })
    expect(hauls).toHaveAttribute('href', '/hauls')
    expect(hauls).toHaveTextContent('Isolated workspaces, each with its own content store')
    expect(screen.queryByText('Manage haul archive files')).not.toBeInTheDocument()
    await screen.findByText('/data')
  })
})
