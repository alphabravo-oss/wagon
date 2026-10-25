import { useState, useEffect, useCallback } from 'react'
import { Package, Folder } from 'lucide-react'
import { NavLink } from 'react-router-dom'
import { useHauls } from '../contexts/useHauls.js'

// Defaults avoid the ports Wagon already uses in the same container: the UI on
// 8080 and Publishing's registry front door on 5000. Keep in sync with
// backend/internal/serve (DefaultRegistryPort, DefaultFileserverPort).
const DEFAULT_REGISTRY_PORT = 5002
const DEFAULT_FILESERVER_PORT = 5001

// The API answers errors with plain text (http.Error), not JSON.
async function errorText(res, fallback) {
  const text = (await res.text()).trim()
  try {
    const data = JSON.parse(text)
    return data.message || data.error || fallback
  } catch {
    return text || fallback
  }
}

function Serve() {
  const { activeHaul } = useHauls()
  // Registry state
  const [registryPort, setRegistryPort] = useState(DEFAULT_REGISTRY_PORT)
  const [registryReadonly, setRegistryReadonly] = useState(true)
  const [registryAutoTls, setRegistryAutoTls] = useState(false)
  const [registryTlsCert, setRegistryTlsCert] = useState('')
  const [registryTlsKey, setRegistryTlsKey] = useState('')
  const [registryDirectory, setRegistryDirectory] = useState('')
  const [registryShowAdvanced, setRegistryShowAdvanced] = useState(false)
  const [registryProcesses, setRegistryProcesses] = useState([])
  const [registryLoading, setRegistryLoading] = useState(true)

  // Fileserver state
  const [fileserverPort, setFileserverPort] = useState(DEFAULT_FILESERVER_PORT)
  const [fileserverTimeout, setFileserverTimeout] = useState('')
  const [fileserverAutoTls, setFileserverAutoTls] = useState(false)
  const [fileserverTlsCert, setFileserverTlsCert] = useState('')
  const [fileserverTlsKey, setFileserverTlsKey] = useState('')
  const [fileserverDirectory, setFileserverDirectory] = useState('')
  const [fileserverShowAdvanced, setFileserverShowAdvanced] = useState(false)
  const [fileserverProcesses, setFileserverProcesses] = useState([])
  const [fileserverLoading, setFileserverLoading] = useState(true)

  // Shared UI state
  const [error, setError] = useState(null)
  const [submitting, setSubmitting] = useState(false)

  // Process lists are scoped to the active haul. The fetchers depend on its id
  // so switching hauls re-fetches immediately and the polling interval below is
  // re-created instead of continuing to poll the previously active haul.
  const activeHaulId = activeHaul?.id
  const haulQuery = activeHaulId != null ? `?haul=${activeHaulId}` : ''

  // Fetch registry processes
  const fetchRegistryProcesses = useCallback(async () => {
    try {
      const res = await fetch(`/api/serve/registry${haulQuery}`)
      if (res.ok) {
        const data = await res.json()
        setRegistryProcesses(data)
      }
    } catch (err) {
      console.error('Failed to fetch registry processes:', err)
    } finally {
      setRegistryLoading(false)
    }
  }, [haulQuery])

  // Fetch fileserver processes
  const fetchFileserverProcesses = useCallback(async () => {
    try {
      const res = await fetch(`/api/serve/fileserver${haulQuery}`)
      if (res.ok) {
        const data = await res.json()
        setFileserverProcesses(data)
      }
    } catch (err) {
      console.error('Failed to fetch fileserver processes:', err)
    } finally {
      setFileserverLoading(false)
    }
  }, [haulQuery])

  useEffect(() => {
    fetchRegistryProcesses()
    fetchFileserverProcesses()
    const interval = setInterval(() => {
      fetchRegistryProcesses()
      fetchFileserverProcesses()
    }, 5000)
    return () => clearInterval(interval)
  }, [fetchRegistryProcesses, fetchFileserverProcesses])

  const handleStartRegistry = async (e) => {
    e.preventDefault()
    setError(null)
    setSubmitting(true)

    try {
      const requestPayload = {
        haulId: activeHaul?.id,
        port: registryPort || DEFAULT_REGISTRY_PORT,
        readonly: registryReadonly,
        tlsCert: registryAutoTls ? undefined : (registryTlsCert || undefined),
        tlsKey: registryAutoTls ? undefined : (registryTlsKey || undefined),
        autoTls: registryAutoTls || undefined,
        directory: registryDirectory || undefined,
      }

      Object.keys(requestPayload).forEach(key => {
        if (requestPayload[key] === undefined) {
          delete requestPayload[key]
        }
      })

      const res = await fetch('/api/serve/registry', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(requestPayload)
      })

      if (!res.ok) {
        throw new Error(await errorText(res, 'Start registry failed'))
      }

      await fetchRegistryProcesses()
    } catch (err) {
      setError(err.message)
    } finally {
      setSubmitting(false)
    }
  }

  const handleStopRegistry = async (pid) => {
    setError(null)
    setSubmitting(true)

    try {
      const res = await fetch(`/api/serve/registry/${pid}`, {
        method: 'DELETE'
      })

      if (!res.ok) {
        throw new Error(await errorText(res, 'Stop registry failed'))
      }

      await fetchRegistryProcesses()
    } catch (err) {
      setError(err.message)
    } finally {
      setSubmitting(false)
    }
  }

  const handleStartFileserver = async (e) => {
    e.preventDefault()
    setError(null)
    setSubmitting(true)

    try {
      const requestPayload = {
        haulId: activeHaul?.id,
        port: fileserverPort || DEFAULT_FILESERVER_PORT,
        timeout: fileserverTimeout ? parseInt(fileserverTimeout) : undefined,
        tlsCert: fileserverAutoTls ? undefined : (fileserverTlsCert || undefined),
        tlsKey: fileserverAutoTls ? undefined : (fileserverTlsKey || undefined),
        autoTls: fileserverAutoTls || undefined,
        directory: fileserverDirectory || undefined,
      }

      Object.keys(requestPayload).forEach(key => {
        if (requestPayload[key] === undefined) {
          delete requestPayload[key]
        }
      })

      const res = await fetch('/api/serve/fileserver', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(requestPayload)
      })

      if (!res.ok) {
        throw new Error(await errorText(res, 'Start fileserver failed'))
      }

      await fetchFileserverProcesses()
    } catch (err) {
      setError(err.message)
    } finally {
      setSubmitting(false)
    }
  }

  const handleStopFileserver = async (pid) => {
    setError(null)
    setSubmitting(true)

    try {
      const res = await fetch(`/api/serve/fileserver/${pid}`, {
        method: 'DELETE'
      })

      if (!res.ok) {
        throw new Error(await errorText(res, 'Stop fileserver failed'))
      }

      await fetchFileserverProcesses()
    } catch (err) {
      setError(err.message)
    } finally {
      setSubmitting(false)
    }
  }

  const getStatusBadgeClass = (status) => {
    switch (status) {
      case 'running':
        return 'badge-success'
      case 'stopped':
        return 'badge-info'
      case 'crashed':
        return 'badge-error'
      default:
        return 'badge-info'
    }
  }

  return (
    <div className="page">
      <div className="page-header">
        <div>
          <h1 className="page-title">Serve</h1>
          <p className="page-subtitle">
            Advanced: run one hauler registry or fileserver by hand. To serve hauls to a
            cluster, use <NavLink to="/publish" style={{ color: 'var(--accent-amber)' }}>Publishing</NavLink> (see docs/serving.md).
          </p>
        </div>
      </div>

      {error && (
        <div className="card" style={{ borderColor: 'var(--accent-red)', marginBottom: '1rem' }}>
          <div className="card-title" style={{ color: 'var(--accent-red)' }}>Error</div>
          <p style={{ color: 'var(--text-secondary)' }}>{error}</p>
        </div>
      )}

      {/* Two-column layout */}
      <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(350px, 1fr))', gap: '1.5rem' }}>

        {/* Registry Column */}
        <div className="serve-column">
          <div className="card">
            <div className="card-title" style={{ display: 'flex', alignItems: 'center', gap: '0.5rem' }}>
              <Package size={20} style={{ color: 'var(--accent-amber-dim)' }} />
              Registry
            </div>

            <form onSubmit={handleStartRegistry}>
              <div className="form-group">
                <label className="form-label">Port</label>
                <input
                  className="form-input"
                  type="number"
                  min="1"
                  max="65535"
                  placeholder={String(DEFAULT_REGISTRY_PORT)}
                  value={registryPort}
                  onChange={(e) => setRegistryPort(parseInt(e.target.value) || DEFAULT_REGISTRY_PORT)}
                  disabled={submitting}
                />
                <div style={{ fontSize: '0.75rem', color: 'var(--text-muted)', marginTop: '0.35rem' }}>
                  Port for the registry (default: {DEFAULT_REGISTRY_PORT}; 5000 is Publishing&apos;s)
                </div>
              </div>

              <div className="form-group">
                <label className="form-label" style={{ display: 'flex', alignItems: 'center', gap: '0.5rem' }}>
                  <input
                    type="checkbox"
                    checked={registryReadonly}
                    onChange={(e) => setRegistryReadonly(e.target.checked)}
                    disabled={submitting}
                    style={{ width: 'auto' }}
                  />
                  <span>Readonly</span>
                </label>
                <div style={{ fontSize: '0.75rem', color: 'var(--text-muted)', marginTop: '0.35rem' }}>
                  Read-only mode (default: true)
                </div>
              </div>

              <div className="form-group">
                <label className="form-label" style={{ display: 'flex', alignItems: 'center', gap: '0.5rem' }}>
                  <input
                    type="checkbox"
                    checked={registryAutoTls}
                    onChange={(e) => setRegistryAutoTls(e.target.checked)}
                    disabled={submitting}
                    style={{ width: 'auto' }}
                  />
                  <span>Enable TLS with self-signed certificate</span>
                </label>
                <div style={{ fontSize: '0.75rem', color: 'var(--text-muted)', marginTop: '0.35rem' }}>
                  Auto-generate and use a self-signed certificate for HTTPS
                </div>
              </div>

              <button
                type="button"
                className="btn btn-sm"
                onClick={() => setRegistryShowAdvanced(!registryShowAdvanced)}
                style={{ marginTop: '0.5rem' }}
              >
                {registryShowAdvanced ? '▼ Hide Advanced' : '▶ Show Advanced'}
              </button>

              {registryShowAdvanced && (
                <div style={{ marginTop: '1rem', paddingTop: '1rem', borderTop: '1px solid var(--border-color)' }}>
                  {!registryAutoTls && (
                    <>
                      <div className="form-group">
                        <label className="form-label">TLS Certificate Path</label>
                        <input
                          className="form-input"
                          placeholder="/path/to/cert.pem"
                          value={registryTlsCert}
                          onChange={(e) => setRegistryTlsCert(e.target.value)}
                          disabled={submitting}
                        />
                      </div>

                      <div className="form-group">
                        <label className="form-label">TLS Key Path</label>
                        <input
                          className="form-input"
                          placeholder="/path/to/key.pem"
                          value={registryTlsKey}
                          onChange={(e) => setRegistryTlsKey(e.target.value)}
                          disabled={submitting}
                        />
                      </div>
                    </>
                  )}

                  <div className="form-group">
                    <label className="form-label">Store Directory</label>
                    <input
                      className="form-input"
                      placeholder="/path/to/store"
                      value={registryDirectory}
                      onChange={(e) => setRegistryDirectory(e.target.value)}
                      disabled={submitting}
                    />
                  </div>
                </div>
              )}

              <button
                type="submit"
                className="btn btn-primary"
                disabled={submitting}
                style={{ width: '100%', marginTop: '1rem' }}
              >
                {submitting ? 'Starting...' : 'Start Registry'}
              </button>
            </form>
          </div>

          {/* Registry Processes */}
          <div className="card" style={{ marginTop: '1rem' }}>
            <div className="card-title">Processes</div>
            {registryLoading ? (
              <div style={{ color: 'var(--text-secondary)' }}>Loading...</div>
            ) : registryProcesses.length === 0 ? (
              <div style={{ color: 'var(--text-secondary)' }}>No registry processes running</div>
            ) : (
              <table className="data-table">
                <thead>
                  <tr>
                    <th>PID</th>
                    <th>Port</th>
                    <th>Status</th>
                    <th>Actions</th>
                  </tr>
                </thead>
                <tbody>
                  {registryProcesses.map(proc => (
                    <tr key={proc.id}>
                      <td className="primary">#{proc.pid}</td>
                      <td>{proc.port}</td>
                      <td>
                        <span className={`badge ${getStatusBadgeClass(proc.status)}`}>{proc.status}</span>
                        {proc.status === 'crashed' && proc.exitReason && (
                          <div className="serve-exit-reason" style={{ fontSize: '0.75rem', color: 'var(--accent-red)', marginTop: '0.25rem' }}>
                            {proc.exitReason}
                          </div>
                        )}
                      </td>
                      <td>
                        {proc.status === 'running' && (
                          <button
                            className="btn btn-sm"
                            onClick={() => handleStopRegistry(proc.pid)}
                            disabled={submitting}
                          >
                            Stop
                          </button>
                        )}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            )}
          </div>

          {/* Registry Access Info */}
          {registryProcesses.length > 0 && (() => {
            const proc = registryProcesses.find(p => p.status === 'running') || registryProcesses[0]
            const hasTls = proc?.args?.autoTls || proc?.args?.tlsCert
            const protocol = hasTls ? 'https' : 'http'
            const port = proc?.port || registryPort
            return (
              <div className="card" style={{ marginTop: '1rem' }}>
                <div className="card-title">Access Info</div>
                <div style={{ fontSize: '0.8rem', color: 'var(--text-secondary)', lineHeight: '1.6' }}>
                  <p style={{ marginBottom: '0.5rem' }}>Access the registry at:</p>
                  <code style={{ display: 'block', padding: '0.5rem', backgroundColor: 'var(--bg-primary)', borderRadius: '4px' }}>
                    {protocol}://localhost:{port}
                  </code>
                  <p style={{ marginTop: '0.75rem', marginBottom: '0' }}>
                    Pull images with: <code>docker pull localhost:{port}/&lt;repo&gt;:&lt;tag&gt;</code>
                  </p>
                  {hasTls && (
                    <p style={{ marginTop: '0.5rem', fontSize: '0.75rem', color: 'var(--text-muted)' }}>
                      Note: Using self-signed certificate. Browser may show a security warning.
                    </p>
                  )}
                </div>
              </div>
            )
          })()}
        </div>

        {/* Fileserver Column */}
        <div className="serve-column">
          <div className="card">
            <div className="card-title" style={{ display: 'flex', alignItems: 'center', gap: '0.5rem' }}>
              <Folder size={20} style={{ color: 'var(--accent-amber-dim)' }} />
              Fileserver
            </div>

            <form onSubmit={handleStartFileserver}>
              <div className="form-group">
                <label className="form-label">Port</label>
                <input
                  className="form-input"
                  type="number"
                  min="1"
                  max="65535"
                  placeholder={String(DEFAULT_FILESERVER_PORT)}
                  value={fileserverPort}
                  onChange={(e) => setFileserverPort(parseInt(e.target.value) || DEFAULT_FILESERVER_PORT)}
                  disabled={submitting}
                />
                <div style={{ fontSize: '0.75rem', color: 'var(--text-muted)', marginTop: '0.35rem' }}>
                  Port for the fileserver (default: {DEFAULT_FILESERVER_PORT}; 8080 is the UI&apos;s)
                </div>
              </div>

              <div className="form-group">
                <label className="form-label">Timeout</label>
                <input
                  className="form-input"
                  type="number"
                  min="0"
                  placeholder="0"
                  value={fileserverTimeout}
                  onChange={(e) => setFileserverTimeout(e.target.value)}
                  disabled={submitting}
                />
                <div style={{ fontSize: '0.75rem', color: 'var(--text-muted)', marginTop: '0.35rem' }}>
                  Timeout in seconds (default: 0 / no timeout)
                </div>
              </div>

              <div className="form-group">
                <label className="form-label" style={{ display: 'flex', alignItems: 'center', gap: '0.5rem' }}>
                  <input
                    type="checkbox"
                    checked={fileserverAutoTls}
                    onChange={(e) => setFileserverAutoTls(e.target.checked)}
                    disabled={submitting}
                    style={{ width: 'auto' }}
                  />
                  <span>Enable TLS with self-signed certificate</span>
                </label>
                <div style={{ fontSize: '0.75rem', color: 'var(--text-muted)', marginTop: '0.35rem' }}>
                  Auto-generate and use a self-signed certificate for HTTPS
                </div>
              </div>

              <button
                type="button"
                className="btn btn-sm"
                onClick={() => setFileserverShowAdvanced(!fileserverShowAdvanced)}
                style={{ marginTop: '0.5rem' }}
              >
                {fileserverShowAdvanced ? '▼ Hide Advanced' : '▶ Show Advanced'}
              </button>

              {fileserverShowAdvanced && (
                <div style={{ marginTop: '1rem', paddingTop: '1rem', borderTop: '1px solid var(--border-color)' }}>
                  {!fileserverAutoTls && (
                    <>
                      <div className="form-group">
                        <label className="form-label">TLS Certificate Path</label>
                        <input
                          className="form-input"
                          placeholder="/path/to/cert.pem"
                          value={fileserverTlsCert}
                          onChange={(e) => setFileserverTlsCert(e.target.value)}
                          disabled={submitting}
                        />
                      </div>

                      <div className="form-group">
                        <label className="form-label">TLS Key Path</label>
                        <input
                          className="form-input"
                          placeholder="/path/to/key.pem"
                          value={fileserverTlsKey}
                          onChange={(e) => setFileserverTlsKey(e.target.value)}
                          disabled={submitting}
                        />
                      </div>
                    </>
                  )}

                  <div className="form-group">
                    <label className="form-label">Store Directory</label>
                    <input
                      className="form-input"
                      placeholder="/path/to/store"
                      value={fileserverDirectory}
                      onChange={(e) => setFileserverDirectory(e.target.value)}
                      disabled={submitting}
                    />
                  </div>
                </div>
              )}

              <button
                type="submit"
                className="btn btn-primary"
                disabled={submitting}
                style={{ width: '100%', marginTop: '1rem' }}
              >
                {submitting ? 'Starting...' : 'Start Fileserver'}
              </button>
            </form>
          </div>

          {/* Fileserver Processes */}
          <div className="card" style={{ marginTop: '1rem' }}>
            <div className="card-title">Processes</div>
            {fileserverLoading ? (
              <div style={{ color: 'var(--text-secondary)' }}>Loading...</div>
            ) : fileserverProcesses.length === 0 ? (
              <div style={{ color: 'var(--text-secondary)' }}>No fileserver processes running</div>
            ) : (
              <table className="data-table">
                <thead>
                  <tr>
                    <th>PID</th>
                    <th>Port</th>
                    <th>Status</th>
                    <th>Actions</th>
                  </tr>
                </thead>
                <tbody>
                  {fileserverProcesses.map(proc => (
                    <tr key={proc.id}>
                      <td className="primary">#{proc.pid}</td>
                      <td>{proc.port}</td>
                      <td>
                        <span className={`badge ${getStatusBadgeClass(proc.status)}`}>{proc.status}</span>
                        {proc.status === 'crashed' && proc.exitReason && (
                          <div className="serve-exit-reason" style={{ fontSize: '0.75rem', color: 'var(--accent-red)', marginTop: '0.25rem' }}>
                            {proc.exitReason}
                          </div>
                        )}
                      </td>
                      <td>
                        {proc.status === 'running' && (
                          <button
                            className="btn btn-sm"
                            onClick={() => handleStopFileserver(proc.pid)}
                            disabled={submitting}
                          >
                            Stop
                          </button>
                        )}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            )}
          </div>

          {/* Fileserver Access Info */}
          {fileserverProcesses.length > 0 && (() => {
            const proc = fileserverProcesses.find(p => p.status === 'running') || fileserverProcesses[0]
            const hasTls = proc?.args?.autoTls || proc?.args?.tlsCert
            const protocol = hasTls ? 'https' : 'http'
            const port = proc?.port || fileserverPort
            return (
              <div className="card" style={{ marginTop: '1rem' }}>
                <div className="card-title">Access Info</div>
                <div style={{ fontSize: '0.8rem', color: 'var(--text-secondary)', lineHeight: '1.6' }}>
                  <p style={{ marginBottom: '0.5rem' }}>Access the fileserver at:</p>
                  <code style={{ display: 'block', padding: '0.5rem', backgroundColor: 'var(--bg-primary)', borderRadius: '4px' }}>
                    {protocol}://localhost:{port}
                  </code>
                  <p style={{ marginTop: '0.75rem', marginBottom: '0' }}>
                    Download files with: <code>curl {protocol}://localhost:{port}/&lt;file-path&gt;</code>
                  </p>
                  {hasTls && (
                    <p style={{ marginTop: '0.5rem', fontSize: '0.75rem', color: 'var(--text-muted)' }}>
                      Note: Using self-signed certificate. Use <code>-k</code> flag with curl to bypass certificate verification.
                    </p>
                  )}
                </div>
              </div>
            )
          })()}
        </div>
      </div>

      {/* About Section */}
      <div className="card" style={{ marginTop: '1.5rem' }}>
        <div className="card-title">About Serve</div>
        <div style={{ color: 'var(--text-secondary)', fontSize: '0.9rem', lineHeight: '1.6' }}>
          <p style={{ marginBottom: '0.75rem' }}>
            <strong>Registry:</strong> Starts an embedded container registry that serves images from your hauler store.
            Useful for local testing.
          </p>
          <p>
            <strong>Fileserver:</strong> Starts an embedded HTTP file server that serves charts, files, and other content
            from your hauler store via HTTP.
          </p>
          <p style={{ marginTop: '0.75rem' }}>
            These run inside the Wagon container, so publish the port you pick (for example{' '}
            <code>-p 127.0.0.1:{DEFAULT_REGISTRY_PORT}:{DEFAULT_REGISTRY_PORT}</code>). They stop when Wagon restarts.
          </p>
        </div>
      </div>
    </div>
  )
}

export default Serve
