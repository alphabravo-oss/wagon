import { useState, useEffect, useRef } from 'react'
import { useNavigate } from 'react-router-dom'
import { ChevronDown, Layers } from 'lucide-react'
import { useHauls } from '../contexts/useHauls.js'

function HaulSwitcher() {
  const { hauls, activeHaul, setActiveHaulId } = useHauls()
  const [open, setOpen] = useState(false)
  const navigate = useNavigate()
  const ref = useRef(null)

  useEffect(() => {
    const onClick = (e) => {
      if (ref.current && !ref.current.contains(e.target)) {
        setOpen(false)
      }
    }
    document.addEventListener('mousedown', onClick)
    return () => document.removeEventListener('mousedown', onClick)
  }, [])

  const handleSelect = (haul) => {
    setActiveHaulId(haul.id)
    setOpen(false)
    navigate(`/hauls/${haul.id}`)
  }

  return (
    <div ref={ref} style={{ position: 'relative' }}>
      <button
        className="btn btn-sm"
        onClick={() => setOpen(!open)}
        title="Active haul"
        style={{ display: 'flex', alignItems: 'center', gap: '0.4rem', maxWidth: '220px' }}
      >
        <Layers size={14} style={{ color: 'var(--accent-amber)' }} />
        <span style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
          {activeHaul ? activeHaul.name : 'No haul selected'}
        </span>
        <ChevronDown size={14} />
      </button>
      {open && (
        <div
          style={{
            position: 'absolute', top: 'calc(100% + 4px)', right: 0, zIndex: 50,
            minWidth: '240px', maxHeight: '320px', overflowY: 'auto',
            background: 'var(--bg-secondary)', border: '1px solid var(--border-color)',
            borderRadius: '6px', boxShadow: '0 8px 24px rgba(0,0,0,0.4)', padding: '0.35rem',
          }}
        >
          {hauls.length === 0 ? (
            <div style={{ padding: '0.5rem 0.75rem', color: 'var(--text-muted)', fontSize: '0.85rem' }}>
              No hauls yet
            </div>
          ) : (
            hauls.map((haul) => (
              <button
                key={haul.id}
                onClick={() => handleSelect(haul)}
                style={{
                  display: 'flex', alignItems: 'center', justifyContent: 'space-between',
                  width: '100%', padding: '0.5rem 0.65rem', background: haul.id === activeHaul?.id ? 'var(--bg-tertiary)' : 'transparent',
                  border: 'none', borderRadius: '4px', cursor: 'pointer', color: 'var(--text-primary)',
                  fontSize: '0.85rem', textAlign: 'left',
                }}
              >
                <span style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{haul.name}</span>
                <span style={{ fontSize: '0.7rem', color: 'var(--text-muted)' }}>
                  {(haul.imageCount || 0) + (haul.chartCount || 0) + (haul.fileCount || 0)} items
                </span>
              </button>
            ))
          )}
          <div style={{ borderTop: '1px solid var(--border-color)', marginTop: '0.35rem', paddingTop: '0.35rem' }}>
            <button
              onClick={() => { setOpen(false); navigate('/hauls') }}
              style={{
                display: 'block', width: '100%', padding: '0.5rem 0.65rem', background: 'transparent',
                border: 'none', borderRadius: '4px', cursor: 'pointer', color: 'var(--accent-amber)',
                fontSize: '0.85rem', textAlign: 'left',
              }}
            >
              Manage hauls →
            </button>
          </div>
        </div>
      )}
    </div>
  )
}

export default HaulSwitcher
