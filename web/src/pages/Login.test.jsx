import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import Login from './Login.jsx'

const response = (data, ok = true) => ({ ok, json: async () => data })

describe('Login', () => {
  let fetchMock
  beforeEach(() => {
    fetchMock = vi.fn().mockResolvedValueOnce(response({ authenticated: false, authEnabled: true }))
    vi.stubGlobal('fetch', fetchMock)
  })
  afterEach(() => {
    cleanup()
    vi.unstubAllGlobals()
  })

  function renderLogin() {
    render(
      <MemoryRouter initialEntries={['/login']}>
        <Routes>
          <Route path="/login" element={<Login />} />
          <Route path="/" element={<h1>Dashboard</h1>} />
        </Routes>
      </MemoryRouter>,
    )
  }

  function submitPassword() {
    fireEvent.change(screen.getByLabelText('Password'), { target: { value: 'typed-password' } })
    fireEvent.click(screen.getByRole('button', { name: 'Sign In' }))
  }

  it('renders the required password field and submit button', async () => {
    renderLogin()
    expect(await screen.findByLabelText('Password')).toHaveAttribute('type', 'password')
    expect(screen.getByLabelText('Password')).toBeRequired()
    expect(screen.getByRole('button', { name: 'Sign In' })).toHaveAttribute('type', 'submit')
    expect(fetchMock).toHaveBeenCalledExactlyOnceWith('/api/auth/validate')
  })

  it('POSTs the typed password and navigates to the dashboard on success', async () => {
    fetchMock.mockResolvedValueOnce(response({ success: true }))
    renderLogin()
    submitPassword()
    expect(await screen.findByRole('heading', { name: 'Dashboard' })).toBeInTheDocument()
    expect(fetchMock).toHaveBeenLastCalledWith('/api/auth/login', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ password: 'typed-password' }),
    })
    expect(screen.queryByLabelText('Password')).not.toBeInTheDocument()
  })

  it.each([
    [false, { message: 'Incorrect password' }, 'Incorrect password'],
    [true, { success: false }, 'Login failed'],
  ])('shows the login error and stays on the form (HTTP ok=%s)', async (ok, data, message) => {
    fetchMock.mockResolvedValueOnce(response(data, ok))
    renderLogin()
    submitPassword()
    expect(await screen.findByText(message)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Sign In' })).toBeEnabled()
    expect(screen.queryByRole('heading', { name: 'Dashboard' })).not.toBeInTheDocument()
  })

  it('shows a network error when login cannot reach the server', async () => {
    fetchMock.mockRejectedValueOnce(new Error('Connection lost'))
    renderLogin()
    submitPassword()
    expect(await screen.findByText('Connection lost')).toBeInTheDocument()
    expect(screen.getByLabelText('Password')).toBeEnabled()
  })
})
