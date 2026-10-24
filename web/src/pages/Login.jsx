import { useState, useEffect } from 'react'
import { useNavigate } from 'react-router-dom'
import Brand from '../components/Brand.jsx'
import { useAuth } from '../contexts/useAuth.js'
import './Login.css'

export default function Login() {
  const [password, setPassword] = useState('')
  const [error, setError] = useState(null)
  const [loading, setLoading] = useState(false)
  const [authEnabled, setAuthEnabled] = useState(true)
  const navigate = useNavigate()
  const { checkAuth } = useAuth()

  useEffect(() => {
    // Check if auth is enabled and if user is already authenticated
    fetch('/api/auth/validate')
      .then(res => res.json())
      .then(data => {
        setAuthEnabled(data.authEnabled)
        if (data.authenticated || !data.authEnabled) {
          navigate('/')
        }
      })
      .catch(() => {
        // If validation fails, assume auth is enabled and show login
        setAuthEnabled(true)
      })
  }, [navigate])

  const handleSubmit = async (e) => {
    e.preventDefault()
    setError(null)
    setLoading(true)

    try {
      const res = await fetch('/api/auth/login', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ password })
      })

      const data = await res.json()

      if (!res.ok || !data.success) {
        throw new Error(data.message || 'Login failed')
      }

      // Refresh the shared auth state before leaving, or ProtectedRoute still
      // sees the pre-login state and sends the browser back to /login.
      await checkAuth()
      navigate('/')
    } catch (err) {
      setError(err.message)
    } finally {
      setLoading(false)
    }
  }

  if (!authEnabled) {
    return (
      <div className="login-page">
        <div className="login-card">
          <div className="login-header">
            <div className="login-brand">
              <Brand />
            </div>
            <h1 className="login-title">Authentication Not Required</h1>
          </div>
          <div className="login-body">
            <p style={{ color: 'var(--text-secondary)', marginBottom: '1rem' }}>
              Authentication is not configured for this instance.
            </p>
            <button className="btn btn-primary" onClick={() => navigate('/')}>
              Continue to Dashboard
            </button>
          </div>
        </div>
      </div>
    )
  }

  return (
    <div className="login-page">
      <div className="login-card">
        <div className="login-header">
          <div className="login-brand">
            <Brand />
          </div>
          <h1 className="login-title">Sign In</h1>
          <p className="login-subtitle">Enter your password to access the UI</p>
        </div>

        {error && (
          <div className="login-error">
            {error}
          </div>
        )}

        <form onSubmit={handleSubmit} className="login-form">
          <div className="form-group">
            <label className="form-label" htmlFor="password">Password</label>
            <input
              id="password"
              className="form-input"
              type="password"
              placeholder="Enter password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              disabled={loading}
              autoFocus
              required
            />
          </div>

          <button type="submit" className="btn btn-primary btn-block" disabled={loading}>
            {loading ? 'Signing in...' : 'Sign In'}
          </button>
        </form>

        <div className="login-footer">
          <p style={{ color: 'var(--text-muted)', fontSize: '0.8rem' }}>
            Your session will remain valid for 24 hours.
          </p>
        </div>
      </div>
    </div>
  )
}
