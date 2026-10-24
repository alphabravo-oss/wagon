import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { AuthProvider } from '../contexts/AuthContext.jsx'
import ProtectedRoute from './ProtectedRoute.jsx'
import Login from '../pages/Login.jsx'

// Mirrors the server with a password set: /api/auth/validate is 401 until the
// login POST succeeds, then reports an authenticated session.
function fakeServer() {
  let loggedIn = false
  return vi.fn(async (url, options) => {
    if (url === '/api/auth/login') {
      loggedIn = JSON.parse(options.body).password === 'right-password'
      return loggedIn
        ? { ok: true, status: 200, json: async () => ({ success: true }) }
        : { ok: false, status: 401, json: async () => ({ success: false, message: 'Invalid password' }) }
    }
    if (url === '/api/auth/validate') {
      return loggedIn
        ? { ok: true, status: 200, json: async () => ({ authenticated: true, authEnabled: true }) }
        : { ok: false, status: 401, json: async () => { throw new SyntaxError('Unauthorized is not JSON') } }
    }
    throw new Error(`unexpected fetch ${url}`)
  })
}

describe('first visit with a password set', () => {
  beforeEach(() => vi.stubGlobal('fetch', fakeServer()))
  afterEach(() => {
    cleanup()
    vi.unstubAllGlobals()
  })

  it('sends a new browser to the login form, then to the dashboard after login', async () => {
    render(
      <MemoryRouter initialEntries={['/']}>
        <AuthProvider>
          <Routes>
            <Route path="/login" element={<Login />} />
            <Route path="/" element={<ProtectedRoute><h1>Dashboard</h1></ProtectedRoute>} />
          </Routes>
        </AuthProvider>
      </MemoryRouter>,
    )
    const password = await screen.findByLabelText('Password')
    expect(screen.queryByRole('heading', { name: 'Dashboard' })).not.toBeInTheDocument()

    fireEvent.change(password, { target: { value: 'wrong-password' } })
    fireEvent.click(screen.getByRole('button', { name: 'Sign In' }))
    expect(await screen.findByText('Invalid password')).toBeInTheDocument()

    fireEvent.change(screen.getByLabelText('Password'), { target: { value: 'right-password' } })
    fireEvent.click(screen.getByRole('button', { name: 'Sign In' }))
    expect(await screen.findByRole('heading', { name: 'Dashboard' })).toBeInTheDocument()
  })
})
