import { useState } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { AuthProvider } from './AuthContext.jsx'
import { useAuth } from './useAuth.js'

function AuthConsumer() {
  const { isAuthenticated, authEnabled, loading, login, logout } = useAuth()
  const [result, setResult] = useState('')
  async function signIn() {
    try {
      setResult(await login('secret') ? 'Login succeeded' : 'Login rejected')
    } catch (error) {
      setResult(error.message)
    }
  }
  return (
    <>
      <p>{loading ? 'Resolving auth' : 'Auth ready'}</p>
      <p>{isAuthenticated ? 'Authenticated' : 'Unauthenticated'}</p>
      <p>{authEnabled ? 'Auth enabled' : 'Auth disabled'}</p>
      <button onClick={signIn}>Log in</button>
      <button onClick={logout}>Log out</button>
      <p>{result}</p>
    </>
  )
}

const response = (data, ok = true) => ({ ok, json: async () => data })

describe('AuthProvider', () => {
  let fetchMock
  beforeEach(() => {
    fetchMock = vi.fn()
    vi.stubGlobal('fetch', fetchMock)
  })
  afterEach(() => {
    cleanup()
    vi.unstubAllGlobals()
  })

  async function renderAuth(authenticated = false, authEnabled = true) {
    fetchMock.mockResolvedValueOnce(response({ authenticated, authEnabled }))
    render(<AuthProvider><AuthConsumer /></AuthProvider>)
    await screen.findByText('Auth ready')
  }

  it.each([
    [true, true, 'Authenticated', 'Auth enabled'],
    [false, true, 'Unauthenticated', 'Auth enabled'],
    [false, false, 'Unauthenticated', 'Auth disabled'],
  ])('exposes validation state authenticated=%s authEnabled=%s', async (authenticated, authEnabled, authText, enabledText) => {
    await renderAuth(authenticated, authEnabled)
    // Omitting fetch options uses GET.
    expect(fetchMock).toHaveBeenCalledExactlyOnceWith('/api/auth/validate')
    expect(screen.getByText(authText)).toBeInTheDocument()
    expect(screen.getByText(enabledText)).toBeInTheDocument()
  })

  it('treats a 401 from validate as auth enabled and not logged in', async () => {
    fetchMock.mockResolvedValueOnce({ ok: false, status: 401, json: async () => { throw new Error('not JSON') } })
    render(<AuthProvider><AuthConsumer /></AuthProvider>)
    await screen.findByText('Auth ready')
    expect(screen.getByText('Unauthenticated')).toBeInTheDocument()
    expect(screen.getByText('Auth enabled')).toBeInTheDocument()
  })

  it('POSTs the password and authenticates after successful login', async () => {
    await renderAuth()
    fetchMock.mockResolvedValueOnce(response({ success: true }))
    fireEvent.click(screen.getByRole('button', { name: 'Log in' }))
    await screen.findByText('Login succeeded')
    expect(fetchMock).toHaveBeenLastCalledWith('/api/auth/login', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ password: 'secret' }),
    })
    expect(screen.getByText('Authenticated')).toBeInTheDocument()
  })

  it.each([false, true])('returns false and stays unauthenticated when login fails (HTTP ok=%s)', async (ok) => {
    await renderAuth()
    fetchMock.mockResolvedValueOnce(response({ success: false }, ok))
    fireEvent.click(screen.getByRole('button', { name: 'Log in' }))
    await screen.findByText('Login rejected')
    expect(screen.getByText('Unauthenticated')).toBeInTheDocument()
  })

  it('passes network errors to the login caller and stays unauthenticated', async () => {
    await renderAuth()
    fetchMock.mockRejectedValueOnce(new Error('Network unavailable'))
    fireEvent.click(screen.getByRole('button', { name: 'Log in' }))
    await screen.findByText('Network unavailable')
    expect(screen.getByText('Unauthenticated')).toBeInTheDocument()
  })

  it('POSTs logout and clears authentication', async () => {
    await renderAuth(true)
    fetchMock.mockResolvedValueOnce(response({}))
    fireEvent.click(screen.getByRole('button', { name: 'Log out' }))
    await screen.findByText('Unauthenticated')
    expect(fetchMock).toHaveBeenLastCalledWith('/api/auth/logout', { method: 'POST' })
  })
})
