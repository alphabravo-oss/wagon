import { createContext, useContext, useState, useEffect, useCallback } from 'react'

// === Context for Jobs ===
const JobsContext = createContext()

export function useJobs() {
  const context = useContext(JobsContext)
  if (!context) {
    throw new Error('useJobs must be used within JobsProvider')
  }
  return context
}

export function JobsProvider({ children }) {
  const [jobs, setJobs] = useState([])
  const [runningJobCount, setRunningJobCount] = useState(0)

  // Normalize job data from API (handles both capitalized and lowercase fields)
  const normalizeJob = (data) => ({
    id: data.ID || data.id,
    command: data.Command || data.command,
    args: data.Args || data.args || [],
    status: (data.Status || data.status || '').toLowerCase(),
    exitCode: data.ExitCode ?? data.exitCode,
    startedAt: data.StartedAt || data.startedAt,
    completedAt: data.CompletedAt || data.completedAt,
    createdAt: data.CreatedAt || data.createdAt,
    result: data.Result?.String || data.result,
    envOverrides: data.EnvOverrides || data.envOverrides
  })

  const fetchJobs = useCallback(async () => {
    try {
      const res = await fetch('/api/jobs')
      if (res.ok) {
        const data = await res.json()
        const jobsData = Array.isArray(data) ? data : []
        setJobs(jobsData.map(normalizeJob))
        const running = jobsData.filter(j => {
          const status = (j.Status || j.status || '').toLowerCase()
          return status === 'running' || status === 'queued'
        }).length
        setRunningJobCount(running)
      }
    } catch (err) {
      console.error('Failed to fetch jobs:', err)
    }
  }, [])

  useEffect(() => {
    fetchJobs()
    const interval = setInterval(fetchJobs, 2000)
    return () => clearInterval(interval)
  }, [fetchJobs])

  const deleteAllJobs = async () => {
    const res = await fetch('/api/jobs', { method: 'DELETE' })
    if (res.ok) {
      setJobs([])
      setRunningJobCount(0)
      return true
    }
    throw new Error('Failed to delete jobs')
  }

  return (
    <JobsContext.Provider value={{ jobs, runningJobCount, fetchJobs, deleteAllJobs }}>
      {children}
    </JobsContext.Provider>
  )
}
