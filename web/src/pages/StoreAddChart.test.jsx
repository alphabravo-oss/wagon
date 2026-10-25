import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen, within } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { HaulProvider } from '../contexts/HaulContext.jsx'
import { JobsProvider } from '../contexts/JobsContext.jsx'
import { useHauls } from '../contexts/useHauls.js'
import StoreAddChart from './StoreAddChart.jsx'

function ActiveHaul() {
  const { activeHaul } = useHauls()
  return <p>Active haul: {activeHaul?.name}</p>
}

const response = (data, ok = true) => ({ ok, json: async () => data })

describe('StoreAddChart', () => {
  let fetchMock
  let previousActiveHaul

  beforeEach(() => {
    previousActiveHaul = localStorage.getItem('wagon.activeHaulId')
    localStorage.removeItem('wagon.activeHaulId')
    fetchMock = vi.fn(async (url) => {
      if (url === '/api/hauls') return response({ hauls: [{ id: 7, name: 'Test haul' }] })
      if (url === '/api/jobs') return response([])
      if (url === '/api/hauler/capabilities') return response({ subcommands: [] })
      if (url === '/api/store/add-chart') return response({ jobId: 42 })
      throw new Error(`Unexpected request: ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)
  })

  afterEach(() => {
    cleanup()
    vi.unstubAllGlobals()
    if (previousActiveHaul === null) localStorage.removeItem('wagon.activeHaulId')
    else localStorage.setItem('wagon.activeHaulId', previousActiveHaul)
  })

  async function renderForm() {
    const view = render(
      <MemoryRouter initialEntries={['/store/add-chart']}>
        <HaulProvider>
          <JobsProvider>
            <ActiveHaul />
            <Routes>
              <Route path="/store/add-chart" element={<StoreAddChart />} />
              <Route path="/jobs/42" element={<h1>Chart import job</h1>} />
            </Routes>
          </JobsProvider>
        </HaulProvider>
      </MemoryRouter>,
    )
    await screen.findByText('Active haul: Test haul')
    return view
  }

  // Hauler ignores chart --username/--password and argv would expose them,
  // so the form must not offer or send chart credentials.
  it('offers no chart credential fields, even with advanced options open', async () => {
    const { container } = await renderForm()
    fireEvent.click(screen.getByRole('button', { name: /Show Advanced Options/ }))
    const form = within(container.querySelector('form'))
    expect(form.queryByRole('button', { name: /Authentication Options/ })).not.toBeInTheDocument()
    expect(form.queryByText(/username/i)).not.toBeInTheDocument()
    expect(form.queryByText(/password/i)).not.toBeInTheDocument()
    expect(container.querySelector('input[type="password"]')).toBeNull()
  })

  it('POSTs the chart without credential keys and opens the job', async () => {
    await renderForm()
    fireEvent.change(screen.getByPlaceholderText('nginx-stable/nginx or oci://ghcr.io/mychart'), { target: { value: 'mychart' } })
    fireEvent.change(screen.getByPlaceholderText('https://charts.helm.sh/stable'), { target: { value: 'https://charts.example.com' } })
    fireEvent.change(screen.getByPlaceholderText('1.2.3'), { target: { value: '1.2.3' } })
    fireEvent.click(screen.getByRole('button', { name: 'Add Chart to Store' }))
    expect(await screen.findByRole('heading', { name: 'Chart import job' })).toBeInTheDocument()

    const calls = fetchMock.mock.calls.filter(([url]) => url === '/api/store/add-chart')
    expect(calls).toHaveLength(1)
    const body = JSON.parse(calls[0][1].body)
    expect(body).toEqual({
      haulId: 7,
      name: 'mychart',
      repoUrl: 'https://charts.example.com',
      version: '1.2.3',
      insecureSkipTlsVerify: false,
      plainHttp: false,
      verify: false,
      addDependencies: false,
      addImages: false,
    })
    expect(body).not.toHaveProperty('username')
    expect(body).not.toHaveProperty('password')
  })
})
