import { useNavigate } from 'react-router-dom'
import { useAuth } from '../contexts/useAuth.js'
import Brand from './Brand.jsx'
import HaulSwitcher from './HaulSwitcher.jsx'
import JobIndicator from './JobIndicator.jsx'

function TopBar() {
  const { logout, authEnabled } = useAuth()
  const navigate = useNavigate()

  const handleLogout = async () => {
    await logout()
    navigate('/login')
  }

  return (
    <div className="top-bar">
      <div className="top-bar-left">
        <Brand />
      </div>
      <div className="top-bar-right">
        <HaulSwitcher />
        <JobIndicator />
        {authEnabled && (
          <button className="btn btn-sm" onClick={handleLogout} style={{ marginLeft: '0.5rem' }}>
            Logout
          </button>
        )}
      </div>
    </div>
  )
}

export default TopBar
