import { useState, useEffect } from 'react'
import { NavLink } from 'react-router-dom'

function Dashboard() {
  const [health, setHealth] = useState(null)
  const [capabilities, setCapabilities] = useState(null)

  useEffect(() => {
    fetch('/healthz')
      .then(res => res.json())
      .then(data => setHealth(data))
      .catch(() => setHealth({ status: 'error' }))

    fetch('/api/hauler/capabilities')
      .then(res => res.json())
      .then(data => setCapabilities(data))
  }, [])

  return (
    <div className="page">
      <div className="page-header">
        <div>
          <h1 className="page-title">Dashboard</h1>
          <p className="page-subtitle">Overview of your Wagon instance</p>
        </div>
      </div>

      <div className="card">
        <div className="card-title">System Status</div>
        {health && (
          <div style={{ display: 'flex', flexDirection: 'column', gap: '0.5rem' }}>
            <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem' }}>
              {health.status === 'ok' ? (
                <span className="badge badge-success">Backend OK</span>
              ) : (
                <span className="badge badge-error">
                  Backend {health.status === 'error' ? 'unreachable' : health.status}
                </span>
              )}
            </div>
            {capabilities && (
              <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem' }}>
                <span style={{ color: 'var(--text-secondary)' }}>Hauler CLI:</span>
                <code style={{ fontSize: '0.9rem', color: 'var(--accent-amber)' }}>
                  {capabilities.version.Full || 'Unknown'}
                </code>
              </div>
            )}
          </div>
        )}
      </div>

      <div className="card">
        <div className="card-title">Quick Actions</div>
        <div style={{ display: 'flex', gap: '0.5rem', flexWrap: 'wrap' }}>
          <NavLink to="/store" className="btn">Store Operations</NavLink>
          <NavLink to="/manifests" className="btn">Manage Manifests</NavLink>
          <NavLink to="/jobs" className="btn">Job History</NavLink>
        </div>
      </div>

      <div className="card">
        <div className="card-title">Getting Started</div>
        <p style={{ color: 'var(--text-secondary)', fontSize: '0.9rem', lineHeight: '1.6', marginBottom: '0.75rem' }}>
          Welcome to Wagon. A haul is an isolated workspace with its own content store; a Default haul
          is ready to use.
        </p>
        <ol className="next-steps">
          <li><NavLink to="/hauls">Create a haul</NavLink> for your content, or keep using Default.</li>
          <li><NavLink to="/store/add">Add images</NavLink>, charts, or files to it.</li>
          <li>Build an archive from the haul&apos;s Archives tab, or <NavLink to="/publish">publish it</NavLink> to a cluster.</li>
        </ol>
      </div>
    </div>
  )
}

export default Dashboard
