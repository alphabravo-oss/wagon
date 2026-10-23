import { useEffect } from 'react'
import { useNavigate } from 'react-router-dom'
import { useAuth } from '../contexts/useAuth.js'

function ProtectedRoute({ children }) {
  const { isAuthenticated, authEnabled, loading } = useAuth()
  const navigate = useNavigate()

  useEffect(() => {
    if (!loading) {
      if (authEnabled && !isAuthenticated) {
        navigate('/login')
      }
    }
  }, [isAuthenticated, authEnabled, loading, navigate])

  if (loading) {
    return (
      <div className="page">
        <div className="loading">Loading...</div>
      </div>
    )
  }

  if (authEnabled && !isAuthenticated) {
    return null // Will redirect via useEffect
  }

  return children
}

export default ProtectedRoute
