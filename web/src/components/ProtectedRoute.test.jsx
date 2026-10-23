import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, cleanup, render, screen } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { AuthProvider } from '../contexts/AuthContext.jsx'
import ProtectedRoute from './ProtectedRoute.jsx'

describe('ProtectedRoute', () => {
  let fetchMock
  beforeEach(() => {
    fetchMock = vi.fn()
    vi.stubGlobal('fetch', fetchMock)
  })
  afterEach(() => {
    cleanup()
    vi.unstubAllGlobals()
  })

  function renderRoute() {
    render(
      <MemoryRouter initialEntries={['/private']}>
        <AuthProvider>
          <Routes>
            <Route path="/private" element={<ProtectedRoute><h1>Private content</h1></ProtectedRoute>} />
            <Route path="/login" element={<h1>Login page</h1>} />
          </Routes>
        </AuthProvider>
      </MemoryRouter>,
    )
  }

  it('redirects unauthenticated users to /login', async () => {
    fetchMock.mockResolvedValue({ ok: true, json: async () => ({ authenticated: false, authEnabled: true }) })
    renderRoute()
    expect(await screen.findByRole('heading', { name: 'Login page' })).toBeInTheDocument()
    expect(screen.queryByText('Private content')).not.toBeInTheDocument()
  })

  it.each([
    [true, true],
    [false, false],
  ])('renders children when authenticated=%s and authEnabled=%s', async (authenticated, authEnabled) => {
    fetchMock.mockResolvedValue({ ok: true, json: async () => ({ authenticated, authEnabled }) })
    renderRoute()
    expect(await screen.findByRole('heading', { name: 'Private content' })).toBeInTheDocument()
    expect(screen.queryByText('Login page')).not.toBeInTheDocument()
  })

  it('shows loading until validation resolves, then renders children', async () => {
    let resolveValidation
    fetchMock.mockReturnValue(new Promise(resolve => { resolveValidation = resolve }))
    renderRoute()
    expect(screen.getByText('Loading...')).toBeInTheDocument()
    expect(screen.queryByText('Private content')).not.toBeInTheDocument()
    expect(screen.queryByText('Login page')).not.toBeInTheDocument()
    await act(async () => {
      resolveValidation({ ok: true, json: async () => ({ authenticated: true, authEnabled: true }) })
    })
    expect(await screen.findByRole('heading', { name: 'Private content' })).toBeInTheDocument()
    expect(screen.queryByText('Loading...')).not.toBeInTheDocument()
  })
})
