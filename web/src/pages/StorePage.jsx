import { useState, useEffect } from 'react'
import { NavLink } from 'react-router-dom'
import {
  Image, BarChart3, FileText, RefreshCw, Save, Download, Upload,
  Clipboard, Trash2
} from 'lucide-react'
import { useHauls } from '../contexts/useHauls.js'

function Store() {
  const { activeHaul } = useHauls()
  const [config, setConfig] = useState(null)
  const [error, setError] = useState(null)

  useEffect(() => {
    // Fetch config
    fetch('/api/config')
      .then(res => res.json())
      .then(data => setConfig(data))
      .catch(err => setError('Failed to load config: ' + err.message))
  }, [])

  // Store operations mapped to routes
  const storeOperations = [
    { id: 'add-image', name: 'Add Image', description: 'Add container images to the store', icon: Image, route: '/store/add' },
    { id: 'add-chart', name: 'Add Chart', description: 'Add Helm charts to the store', icon: BarChart3, route: '/store/add-chart' },
    { id: 'add-file', name: 'Add File', description: 'Add local files or remote URLs to the store', icon: FileText, route: '/store/add-file' },
    { id: 'sync', name: 'Sync from Manifests', description: 'Sync store from manifest files', icon: RefreshCw, route: '/store/sync' },
    { id: 'save', name: 'Save Archive', description: 'Package store as a portable archive', icon: Save, route: '/store/save' },
    { id: 'load', name: 'Load Archive', description: 'Load an archive into the store', icon: Download, route: '/store/load' },
    { id: 'extract', name: 'Extract Artifacts', description: 'Extract artifacts from the store', icon: Upload, route: '/store/extract' },
    { id: 'copy', name: 'Copy Store', description: 'Copy store to registry or directory', icon: Clipboard, route: '/store/copy' },
    { id: 'remove', name: 'Remove Artifacts', description: 'Remove artifacts from store (experimental)', icon: Trash2, route: '/store/remove' },
  ]

  // Related pages in the app
  const relatedPages = [
    { name: 'Store Contents', description: 'Browse contents of your content store', route: '/store/contents' },
    { name: 'Hauls', description: 'Isolated workspaces, each with its own content store', route: '/hauls' },
    { name: 'Manifests', description: 'Create and manage hauler manifests', route: '/manifests' },
    { name: 'Registry Login', description: 'Manage container registry credentials', route: '/registry' },
  ]

  return (
    <div className="page">
      <div className="page-header">
        <div>
          <h1 className="page-title">Store Operations</h1>
          <p className="page-subtitle">
            Operations run against {activeHaul ? <strong style={{ color: 'var(--accent-amber)' }}>{activeHaul.name}</strong> : 'the active haul'}
            {' '}— switch hauls from the top bar
          </p>
        </div>
      </div>

      {error && (
        <div className="card" style={{ borderColor: 'var(--accent-red)', marginBottom: '1rem' }}>
          <div className="card-title" style={{ color: 'var(--accent-red)' }}>Error</div>
          <p style={{ color: 'var(--text-secondary)' }}>{error}</p>
        </div>
      )}

      {/* Store Paths */}
      {config && (
        <div className="card">
          <div className="card-title">Store Configuration</div>
          <table className="data-table">
            <tbody>
              <tr>
                <td style={{ width: '180px' }}>Active Haul Store</td>
                <td className="primary">
                  <code>{activeHaul?.storeDir || '-'}</code>
                </td>
                <td style={{ fontSize: '0.75rem', color: 'var(--text-muted)' }}>
                  per-haul (isolated)
                </td>
              </tr>
              <tr>
                <td>Hauler Directory</td>
                <td className="primary">
                  <code>{config.haulerDir || '-'}</code>
                </td>
                <td style={{ fontSize: '0.75rem', color: 'var(--text-muted)' }}>
                  {config.haulerDirEnv || 'HAULER_DIR'}
                </td>
              </tr>
              <tr>
                <td>Temp Directory</td>
                <td className="primary">
                  <code>{config.haulerTempDir || '-'}</code>
                </td>
                <td style={{ fontSize: '0.75rem', color: 'var(--text-muted)' }}>
                  {config.haulerTempEnv || 'HAULER_TEMP_DIR'}
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      )}

      {/* Store Operations */}
      <div className="card">
        <div className="card-title">Operations</div>
        <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(280px, 1fr))', gap: '0.75rem' }}>
          {storeOperations.map(op => (
            <NavLink
              key={op.id}
              to={op.route}
              className="operation-card"
              style={{ textDecoration: 'none' }}
            >
              <div style={{ display: 'flex', alignItems: 'center', gap: '0.75rem' }}>
                <op.icon size={24} style={{ color: 'var(--accent-amber-dim)' }} />
                <div style={{ flex: 1 }}>
                  <div style={{ fontWeight: '500', color: 'var(--text-primary)' }}>{op.name}</div>
                  <div style={{ fontSize: '0.8rem', color: 'var(--text-secondary)' }}>{op.description}</div>
                </div>
              </div>
            </NavLink>
          ))}
        </div>
      </div>

      {/* Related Pages */}
      <div className="card">
        <div className="card-title">Related Pages</div>
        <div style={{ display: 'flex', flexDirection: 'column', gap: '0.5rem' }}>
          {relatedPages.map(page => (
            <NavLink key={page.name} to={page.route} className="related-link">
              <span className="related-link-name">{page.name}</span>
              <span className="related-link-desc">{page.description}</span>
              <span aria-hidden="true">→</span>
            </NavLink>
          ))}
        </div>
      </div>

      {/* Known Limitations */}
      <div className="card" style={{ borderColor: 'var(--accent-amber-dim)' }}>
        <div className="card-title" style={{ color: 'var(--accent-amber)' }}>Known Limitations</div>
        <div style={{ color: 'var(--text-secondary)', fontSize: '0.9rem', lineHeight: '1.6' }}>
          <p style={{ marginBottom: '0.75rem' }}>
            <strong>Podman Tarballs:</strong> Docker-saved tarballs are supported as of hauler v1.3,
            but Podman-generated tarballs are not supported for the <code>store load</code> command.
          </p>
          <p style={{ marginBottom: '0.75rem' }}>
            <strong>Copy to Registry Path:</strong> When using <code>store copy</code> to copy to a registry
            path (e.g., <code>registry://example.com/my-path</code>), you must first login to the registry
            root without the path (e.g., <code>docker.io</code>, not <code>docker.com/my-path</code>).
          </p>
          <p style={{ marginBottom: '0' }}>
            <strong>Temp Directory Space:</strong> Large operations (sync, save) may require significant
            temporary space. Ensure <code>{config?.haulerTempDir || '/data/tmp'}</code> has adequate disk space.
          </p>
        </div>
      </div>
    </div>
  )
}

export default Store
