import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import RegistryLogin from './RegistryLogin.jsx'

vi.mock('../contexts/useJobs.js', () => ({ useJobs: () => ({ fetchJobs: vi.fn() }) }))

describe('RegistryLogin', () => {
  afterEach(() => {
    cleanup()
    vi.unstubAllGlobals()
  })

  it('says stored credentials are base64-encoded, not encrypted', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => ({
      ok: true,
      json: async () => ({ displayPath: '/data/.docker/config.json' }),
    })))
    render(<MemoryRouter><RegistryLogin /></MemoryRouter>)
    expect(await screen.findByText('base64-encoded, not encrypted')).toBeInTheDocument()
    expect(screen.queryByText(/credentials are encrypted/i)).not.toBeInTheDocument()
  })
})
