import { useState } from 'react'
import { NavLink } from 'react-router-dom'
import { Inbox } from 'lucide-react'
import { useJobs } from '../contexts/useJobs.js'
import StatusBadge from '../components/StatusBadge.jsx'

function JobHistory() {
  const { jobs, deleteAllJobs, fetchJobs } = useJobs()
  const [isDeleting, setIsDeleting] = useState(false)

  const handleClearAll = async () => {
    if (!window.confirm('Are you sure you want to delete all jobs? This cannot be undone.')) {
      return
    }
    setIsDeleting(true)
    try {
      await deleteAllJobs()
      await fetchJobs()
    } catch (err) {
      alert('Failed to clear jobs: ' + err.message)
    } finally {
      setIsDeleting(false)
    }
  }

  const formatTime = (dateStr) => {
    if (!dateStr) return '-'
    return new Date(dateStr).toLocaleString()
  }

  const formatDuration = (started, completed) => {
    if (!started || !completed) return '-'
    const start = new Date(started)
    const end = new Date(completed)
    const ms = end - start
    if (ms < 1000) return `${ms}ms`
    if (ms < 60000) return `${(ms / 1000).toFixed(1)}s`
    return `${Math.floor(ms / 60000)}m ${Math.floor((ms % 60000) / 1000)}s`
  }

  return (
    <div className="page">
      <div className="page-header">
        <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', width: '100%' }}>
          <div>
            <h1 className="page-title">Job History</h1>
            <p className="page-subtitle">View and manage background jobs</p>
          </div>
          {jobs.length > 0 && (
            <button
              className="btn btn-danger"
              onClick={handleClearAll}
              disabled={isDeleting}
              style={{ backgroundColor: 'var(--accent-red)', borderColor: 'var(--accent-red)' }}
            >
              {isDeleting ? 'Deleting...' : 'Clear All Jobs'}
            </button>
          )}
        </div>
      </div>

      {jobs.length === 0 ? (
        <div className="empty-state">
          <div className="empty-state-icon"><Inbox size={48} style={{ color: 'var(--text-muted)' }} /></div>
          <div className="empty-state-text">No jobs yet</div>
        </div>
      ) : (
        <table className="data-table">
          <thead>
            <tr>
              <th>ID</th>
              <th>Command</th>
              <th>Status</th>
              <th>Duration</th>
              <th>Created</th>
              <th>Actions</th>
            </tr>
          </thead>
          <tbody>
            {jobs.map(job => (
              <tr key={job.id}>
                <td className="primary">#{job.id}</td>
                <td>
                  <code>{job.command} {(job.args || []).join(' ')}</code>
                </td>
                <td><StatusBadge status={job.status} /></td>
                <td>{formatDuration(job.startedAt, job.completedAt)}</td>
                <td style={{ fontSize: '0.8rem', color: 'var(--text-muted)' }}>
                  {formatTime(job.createdAt)}
                </td>
                <td>
                  <NavLink to={`/jobs/${job.id}`} className="btn btn-sm">View</NavLink>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </div>
  )
}

export default JobHistory
