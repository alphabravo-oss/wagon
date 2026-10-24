import { useState, useEffect, useCallback } from 'react'
import { AuthContext } from './useAuth.js'

export function AuthProvider({ children }) {
  const [isAuthenticated, setIsAuthenticated] = useState(false)
  const [authEnabled, setAuthEnabled] = useState(false)
  const [loading, setLoading] = useState(true)

  const checkAuth = useCallback(async () => {
    try {
      const res = await fetch('/api/auth/validate')
      if (res.ok) {
        const data = await res.json()
        setIsAuthenticated(data.authenticated)
        setAuthEnabled(data.authEnabled)
      } else if (res.status === 401) {
        // The auth middleware rejects /api/auth/validate without a session,
        // which means auth is enabled and this browser is not logged in.
        setIsAuthenticated(false)
        setAuthEnabled(true)
      }
    } catch (err) {
      console.error('Failed to check auth status:', err)
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    checkAuth()
  }, [checkAuth])

  const login = async (password) => {
    const res = await fetch('/api/auth/login', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ password })
    })
    if (res.ok) {
      const data = await res.json()
      if (data.success) {
        setIsAuthenticated(true)
        return true
      }
    }
    return false
  }

  const logout = async () => {
    try {
      await fetch('/api/auth/logout', { method: 'POST' })
    } catch (err) {
      console.error('Logout error:', err)
    }
    setIsAuthenticated(false)
  }

  return (
    <AuthContext.Provider value={{ isAuthenticated, authEnabled, loading, login, logout, checkAuth }}>
      {children}
    </AuthContext.Provider>
  )
}
