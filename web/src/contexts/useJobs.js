import { createContext, useContext } from 'react'

// === Context for Jobs ===
export const JobsContext = createContext()

export function useJobs() {
  const context = useContext(JobsContext)
  if (!context) {
    throw new Error('useJobs must be used within JobsProvider')
  }
  return context
}
