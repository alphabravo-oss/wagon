import { useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { useHauls } from '../contexts/useHauls.js'
import { Package, Plus, Trash2, FolderOpen, Check, Star, Edit2, Image, BarChart3, FileText, FileArchive } from 'lucide-react'

function formatSize(bytes) {
  if (!bytes || bytes === 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  let size = bytes
  let i = 0
  while (size >= 1024 && i < units.length - 1) {
    size /= 1024
    i++
  }
  return `${size.toFixed(size < 10 ? 1 : 0)} ${units[i]}`
}

function Hauls() {
  const { hauls, activeHaulId, setActiveHaulId, loading, createHaul, updateHaul, deleteHaul } = useHauls()
  const navigate = useNavigate()

  const [error, setError] = useState(null)
  const [creating, setCreating] = useState(false)
  const [newName, setNewName] = useState('')
  const [newDesc, setNewDesc] = useState('')
  const [busy, setBusy] = useState(false)
  const [deleteConfirm, setDeleteConfirm] = useState(null)
  const [renameId, setRenameId] = useState(null)
  const [renameValue, setRenameValue] = useState('')

  const handleCreate = async (e) => {
    e.preventDefault()
    setError(null)
    setBusy(true)
    try {
      const haul = await createHaul(newName.trim(), newDesc.trim())
      setNewName('')
      setNewDesc('')
      setCreating(false)
      setActiveHaulId(haul.id)
      navigate(`/hauls/${haul.id}`)
    } catch (err) {
      setError(err.message)
    } finally {
      setBusy(false)
    }
  }

  const handleDelete = async (id) => {
    setError(null)
    setBusy(true)
    try {
      await deleteHaul(id)
      setDeleteConfirm(null)
    } catch (err) {
      setError(err.message)
    } finally {
      setBusy(false)
    }
  }

  const handleRename = async (id) => {
    setError(null)
    setBusy(true)
    try {
      await updateHaul(id, { name: renameValue.trim() })
      setRenameId(null)
    } catch (err) {
      setError(err.message)
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="page">
      <div className="page-header">
        <div>
          <h1 className="page-title">Hauls</h1>
          <p className="page-subtitle">Each haul is an isolated workspace with its own content store</p>
        </div>
        <button className="btn btn-primary" onClick={() => setCreating(!creating)}>
          <Plus size={16} style={{ marginRight: '0.25rem' }} />
          New Haul
        </button>
      </div>

      {error && (
        <div className="card" style={{ borderColor: 'var(--accent-red)', marginBottom: '1rem' }}>
          <div className="card-title" style={{ color: 'var(--accent-red)' }}>Error</div>
          <p style={{ color: 'var(--text-secondary)' }}>{error}</p>
        </div>
      )}

      {creating && (
        <div className="card" style={{ marginBottom: '1rem', borderColor: 'var(--accent-amber-dim)' }}>
          <div className="card-title">Create a New Haul</div>
          <form onSubmit={handleCreate}>
            <div className="form-group">
              <label className="form-label">Name *</label>
              <input
                className="form-input"
                placeholder="e.g. RKE2 Airgap Bundle"
                value={newName}
                onChange={(e) => setNewName(e.target.value)}
                disabled={busy}
                autoFocus
                required
              />
            </div>
            <div className="form-group">
              <label className="form-label">Description (optional)</label>
              <input
                className="form-input"
                placeholder="What does this haul contain?"
                value={newDesc}
                onChange={(e) => setNewDesc(e.target.value)}
                disabled={busy}
              />
            </div>
            <div style={{ display: 'flex', gap: '0.5rem' }}>
              <button type="submit" className="btn btn-primary" disabled={busy || !newName.trim()}>
                {busy ? 'Creating...' : 'Create Haul'}
              </button>
              <button type="button" className="btn" onClick={() => setCreating(false)} disabled={busy}>
                Cancel
              </button>
            </div>
          </form>
        </div>
      )}

      {loading && hauls.length === 0 ? (
        <div className="card"><div className="loading">Loading hauls...</div></div>
      ) : hauls.length === 0 ? (
        <div className="card">
          <div className="empty-state">
            <div className="empty-state-icon"><FolderOpen size={48} style={{ color: 'var(--text-muted)' }} /></div>
            <div className="empty-state-text">No hauls yet</div>
            <p style={{ color: 'var(--text-secondary)', marginTop: '0.5rem' }}>
              A haul is an isolated workspace with its own content store. Create one to start adding images, charts, and files.
            </p>
            <button className="btn btn-primary" style={{ marginTop: '1rem' }} onClick={() => setCreating(true)}>
              <Plus size={16} style={{ marginRight: '0.25rem' }} /> Create Your First Haul
            </button>
          </div>
        </div>
      ) : (
        <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(340px, 1fr))', gap: '1rem' }}>
          {hauls.map((haul) => {
            const isActive = haul.id === activeHaulId
            const totalItems = (haul.imageCount || 0) + (haul.chartCount || 0) + (haul.fileCount || 0)
            return (
              <div
                key={haul.id}
                className="card"
                style={{
                  borderColor: isActive ? 'var(--accent-amber)' : undefined,
                  display: 'flex', flexDirection: 'column', gap: '0.75rem', cursor: 'pointer',
                }}
                onClick={() => { setActiveHaulId(haul.id); navigate(`/hauls/${haul.id}`) }}
              >
                <div style={{ display: 'flex', alignItems: 'flex-start', justifyContent: 'space-between', gap: '0.5rem' }}>
                  <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem', minWidth: 0 }}>
                    <Package size={20} style={{ color: 'var(--accent-amber)', flexShrink: 0 }} />
                    {renameId === haul.id ? (
                      <input
                        className="form-input"
                        value={renameValue}
                        onClick={(e) => e.stopPropagation()}
                        onChange={(e) => setRenameValue(e.target.value)}
                        onKeyDown={(e) => { if (e.key === 'Enter') { e.stopPropagation(); handleRename(haul.id) } }}
                        autoFocus
                        style={{ padding: '0.25rem 0.5rem' }}
                      />
                    ) : (
                      <span style={{ fontWeight: 600, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                        {haul.name}
                      </span>
                    )}
                  </div>
                  {isActive && (
                    <span className="badge badge-warning" style={{ display: 'inline-flex', alignItems: 'center', gap: '0.25rem', flexShrink: 0 }}>
                      <Star size={11} /> Active
                    </span>
                  )}
                </div>

                {haul.description && (
                  <p style={{ color: 'var(--text-secondary)', fontSize: '0.85rem', margin: 0 }}>{haul.description}</p>
                )}

                <div style={{ display: 'flex', gap: '1rem', fontSize: '0.8rem', color: 'var(--text-secondary)', flexWrap: 'wrap' }}>
                  <span style={{ display: 'flex', alignItems: 'center', gap: '0.3rem' }}>
                    <Image size={14} style={{ color: 'var(--accent-blue)' }} /> {haul.imageCount || 0}
                  </span>
                  <span style={{ display: 'flex', alignItems: 'center', gap: '0.3rem' }}>
                    <BarChart3 size={14} style={{ color: 'var(--accent-green)' }} /> {haul.chartCount || 0}
                  </span>
                  <span style={{ display: 'flex', alignItems: 'center', gap: '0.3rem' }}>
                    <FileText size={14} style={{ color: 'var(--accent-amber)' }} /> {haul.fileCount || 0}
                  </span>
                  <span style={{ display: 'flex', alignItems: 'center', gap: '0.3rem' }}>
                    <FileArchive size={14} /> {haul.archiveCount || 0} ({formatSize(haul.archiveBytes)})
                  </span>
                </div>

                <div style={{ borderTop: '1px solid var(--border-color)', paddingTop: '0.6rem', display: 'flex', gap: '0.4rem', flexWrap: 'wrap' }} onClick={(e) => e.stopPropagation()}>
                  <button className="btn btn-sm btn-primary" onClick={() => { setActiveHaulId(haul.id); navigate(`/hauls/${haul.id}`) }}>
                    Open
                  </button>
                  {!isActive && (
                    <button className="btn btn-sm" onClick={() => setActiveHaulId(haul.id)} title="Set as active haul">
                      <Check size={14} style={{ marginRight: '0.2rem' }} /> Set Active
                    </button>
                  )}
                  {renameId === haul.id ? (
                    <>
                      <button className="btn btn-sm btn-primary" onClick={() => handleRename(haul.id)} disabled={busy}>Save</button>
                      <button className="btn btn-sm" onClick={() => setRenameId(null)}>Cancel</button>
                    </>
                  ) : (
                    <button className="btn btn-sm" onClick={() => { setRenameId(haul.id); setRenameValue(haul.name) }} title="Rename">
                      <Edit2 size={14} />
                    </button>
                  )}
                  {totalItems === 0 && (haul.archiveCount || 0) === 0 ? null : null}
                  {deleteConfirm === haul.id ? (
                    <>
                      <button className="btn btn-sm" onClick={() => setDeleteConfirm(null)}>Cancel</button>
                      <button
                        className="btn btn-sm"
                        onClick={() => handleDelete(haul.id)}
                        disabled={busy}
                        style={{ backgroundColor: 'var(--accent-red)', borderColor: 'var(--accent-red)' }}
                      >
                        Confirm Delete
                      </button>
                    </>
                  ) : (
                    <button
                      className="btn btn-sm"
                      onClick={() => setDeleteConfirm(haul.id)}
                      title="Delete haul"
                      style={{ color: 'var(--accent-red)', marginLeft: 'auto' }}
                    >
                      <Trash2 size={14} />
                    </button>
                  )}
                </div>
              </div>
            )
          })}
        </div>
      )}

      <div className="card" style={{ marginTop: '1.5rem' }}>
        <div className="card-title">About Hauls</div>
        <div style={{ fontSize: '0.85rem', color: 'var(--text-secondary)', lineHeight: '1.6' }}>
          <p style={{ marginBottom: '0.5rem' }}>
            A <strong>haul</strong> is an isolated workspace with its own content store. Add images, charts,
            and files to a haul, build it into a portable <code>.tar.zst</code> archive, and publish it — all
            without touching your other hauls.
          </p>
          <p style={{ margin: 0 }}>
            The <strong>active haul</strong> (shown in the top bar) is the default target for store operations.
            Open a haul to manage its contents, archives, and publishing.
          </p>
        </div>
      </div>
    </div>
  )
}

export default Hauls
