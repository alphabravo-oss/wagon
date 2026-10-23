import { useEffect } from 'react'
import { useNavigate } from 'react-router-dom'
import { useJobs } from '../contexts/useJobs.js'

function JobIndicator() {
  const { runningJobCount, fetchJobs } = useJobs()
  const navigate = useNavigate()

  useEffect(() => {
    fetchJobs()
  }, [fetchJobs])

  return (
    <button
      className={`job-indicator ${runningJobCount > 0 ? 'running' : ''}`}
      onClick={() => navigate('/jobs')}
    >
      <span className="status-dot"></span>
      <span>{runningJobCount} job{runningJobCount !== 1 ? 's' : ''} running</span>
    </button>
  )
}

export default JobIndicator
