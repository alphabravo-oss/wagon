import { useState, useEffect } from 'react'
import { useNavigate } from 'react-router-dom'
import { useJobs } from '../contexts/useJobs.js'

function RegistryLogin() {
  const { fetchJobs } = useJobs()
  const navigate = useNavigate()

  const [registry, setRegistry] = useState('')
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState(null)
  const [successMessage, setSuccessMessage] = useState(null)
  const [registryInfo, setRegistryInfo] = useState(null)

  useEffect(() => {
    fetch('/api/registry/info')
      .then(res => res.json())
      .then(data => setRegistryInfo(data))
      .catch(() => setRegistryInfo(null))
  }, [])

  const handleLogin = async (e) => {
    e.preventDefault()
    setError(null)
    setSuccessMessage(null)
    setLoading(true)

    try {
      const res = await fetch('/api/registry/login', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ registry, username, password })
      })

      const data = await res.json()

      if (!res.ok) {
        throw new Error(data.message || 'Login request failed')
      }

      setSuccessMessage(`Login job started for ${registry}. The credentials are saved, not verified.`)
      setRegistry('')
      setUsername('')
      setPassword('')

      // Refresh jobs list and navigate to job detail
      await fetchJobs()
      navigate(`/jobs/${data.jobId}`)
    } catch (err) {
      setError(err.message)
    } finally {
      setLoading(false)
    }
  }

  const handleLogout = async (registryUrl) => {
    setError(null)
    setSuccessMessage(null)
    setLoading(true)

    try {
      const res = await fetch('/api/registry/logout', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ registry: registryUrl })
      })

      const data = await res.json()

      if (!res.ok) {
        throw new Error(data.message || 'Logout request failed')
      }

      setSuccessMessage(`Logout job started for ${registryUrl}`)

      // Refresh jobs list and navigate to job detail
      await fetchJobs()
      navigate(`/jobs/${data.jobId}`)
    } catch (err) {
      setError(err.message)
    } finally {
      setLoading(false)
    }
  }

  return (
    <div className="page">
      <div className="page-header">
        <div>
          <h1 className="page-title">Registry Login</h1>
          <p className="page-subtitle">Manage container registry credentials</p>
        </div>
      </div>

      {error && (
        <div className="card" style={{ borderColor: 'var(--accent-red)', marginBottom: '1rem' }}>
          <div className="card-title" style={{ color: 'var(--accent-red)' }}>Error</div>
          <p style={{ color: 'var(--text-secondary)' }}>{error}</p>
        </div>
      )}

      {successMessage && (
        <div className="card" style={{ borderColor: 'var(--accent-green)', marginBottom: '1rem' }}>
          <div className="card-title" style={{ color: 'var(--accent-green)' }}>Success</div>
          <p style={{ color: 'var(--text-secondary)' }}>{successMessage}</p>
        </div>
      )}

      <div className="card" style={{ maxWidth: '500px' }}>
        <div className="card-title">Login to Registry</div>
        <form onSubmit={handleLogin}>
          <div className="form-group">
            <label className="form-label">Registry URL</label>
            <input
              className="form-input"
              placeholder="docker.io or ghcr.io"
              value={registry}
              onChange={(e) => setRegistry(e.target.value)}
              disabled={loading}
              required
            />
          </div>
          <div className="form-group">
            <label className="form-label">Username</label>
            <input
              className="form-input"
              type="text"
              placeholder="username"
              value={username}
              onChange={(e) => setUsername(e.target.value)}
              disabled={loading}
              required
            />
          </div>
          <div className="form-group">
            <label className="form-label">Password</label>
            <input
              className="form-input"
              type="password"
              placeholder="••••••••"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              disabled={loading}
              required
            />
          </div>
          <button type="submit" className="btn btn-primary" disabled={loading}>
            {loading ? 'Starting Login...' : 'Login'}
          </button>
          <p style={{ color: 'var(--text-secondary)', fontSize: '0.85rem', marginTop: '0.75rem' }}>
            Login saves the credentials without checking them with the registry. A wrong
            password shows up later, when a pull or push fails.
          </p>
        </form>
      </div>

      <div className="card" style={{ maxWidth: '500px', marginTop: '1.5rem' }}>
        <div className="card-title">Quick Logout</div>
        <form onSubmit={(e) => { e.preventDefault(); handleLogout(registry) }}>
          <div className="form-group">
            <label className="form-label">Registry URL</label>
            <input
              className="form-input"
              placeholder="docker.io"
              value={registry}
              onChange={(e) => setRegistry(e.target.value)}
              disabled={loading}
            />
          </div>
          <button type="button" className="btn" onClick={() => handleLogout(registry)} disabled={loading || !registry}>
            Logout
          </button>
        </form>
      </div>

      {registryInfo && (
        <div className="card" style={{ marginTop: '1.5rem' }}>
          <div className="card-title">About Credential Storage</div>
          <div style={{ color: 'var(--text-secondary)', fontSize: '0.9rem', lineHeight: '1.6' }}>
            <p>
              <strong>Note:</strong> Your password is <strong>not stored</strong> in the Wagon database.
              Credentials are managed by hauler and stored in the Docker configuration file.
            </p>
            <p style={{ marginTop: '0.75rem' }}>
              <strong>Storage Location:</strong> <code>{registryInfo.displayPath || registryInfo.dockerAuthPath}</code>
            </p>
            <p style={{ marginTop: '0.75rem', fontSize: '0.85rem' }}>
              Hauler uses the standard Docker auth pattern. Your credentials are stored in the config.json
              file on the persistent data volume. They are <strong>base64-encoded, not encrypted</strong>: anyone
              who can read the volume can read them, so restrict access to it and its backups.
            </p>
          </div>
        </div>
      )}
    </div>
  )
}

export default RegistryLogin
