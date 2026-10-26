import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { HaulProvider } from '../contexts/HaulContext.jsx'
import { JobsProvider } from '../contexts/JobsContext.jsx'
import { useHauls } from '../contexts/useHauls.js'
import StoreAddFile from './StoreAddFile.jsx'

function ActiveHaul() {
  const { activeHaul } = useHauls()
  return <p>Active haul: {activeHaul?.name}</p>
}

const response = (data, ok = true) => ({ ok, json: async () => data })

describe('StoreAddFile', () => {
  let fetchMock
  let addFileResponse
  let previousActiveHaul

  beforeEach(() => {
    previousActiveHaul = localStorage.getItem('wagon.activeHaulId')
    localStorage.removeItem('wagon.activeHaulId')
    addFileResponse = response({ jobId: 42 })
    fetchMock = vi.fn(async (url) => {
      if (url === '/api/hauls') return response({ hauls: [{ id: 7, name: 'Test haul' }] })
      if (url === '/api/jobs') return response([])
      if (url === '/api/store/add-file') return addFileResponse
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
    render(
      <MemoryRouter initialEntries={['/store/add-file']}>
        <HaulProvider>
          <JobsProvider>
            <ActiveHaul />
            <Routes>
              <Route path="/store/add-file" element={<StoreAddFile />} />
              <Route path="/jobs/42" element={<h1>File import job</h1>} />
            </Routes>
          </JobsProvider>
        </HaulProvider>
      </MemoryRouter>,
    )
    await screen.findByText('Active haul: Test haul')
  }

  it('POSTs the file source, custom name and active haul, then opens the job', async () => {
    await renderForm()
    fireEvent.change(screen.getByPlaceholderText('config.yaml'), { target: { value: 'config.yaml' } })
    fireEvent.change(screen.getByPlaceholderText('custom-filename.txt'), { target: { value: 'config.yaml' } })
    fireEvent.click(screen.getByRole('button', { name: 'Add File to Store' }))
    expect(await screen.findByRole('heading', { name: 'File import job' })).toBeInTheDocument()
    expect(fetchMock).toHaveBeenCalledWith('/api/store/add-file', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ haulId: 7, filePath: 'config.yaml', name: 'config.yaml' }),
    })
  })

  it('rejects simultaneous file path and URL without sending a store request', async () => {
    await renderForm()
    expect(screen.getByRole('button', { name: 'Add File to Store' })).toBeDisabled()
    fireEvent.change(screen.getByPlaceholderText('config.yaml'), { target: { value: '/tmp/file.txt' } })
    fireEvent.change(screen.getByPlaceholderText('https://example.com/file.txt'), { target: { value: 'https://example.com/file.txt' } })
    fireEvent.click(screen.getByRole('button', { name: 'Add File to Store' }))
    expect(await screen.findByText('Please provide either a file path or URL, not both')).toBeInTheDocument()
    expect(fetchMock.mock.calls.filter(([url]) => url === '/api/store/add-file')).toHaveLength(0)
    expect(screen.queryByRole('heading', { name: 'File import job' })).not.toBeInTheDocument()
  })

  it('shows an API error and leaves the form available to retry', async () => {
    addFileResponse = response({ message: 'File not found' }, false)
    await renderForm()
    fireEvent.change(screen.getByPlaceholderText('config.yaml'), { target: { value: '/missing.txt' } })
    fireEvent.click(screen.getByRole('button', { name: 'Add File to Store' }))
    expect(await screen.findByText('File not found')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Add File to Store' })).toBeEnabled()
    expect(screen.getByPlaceholderText('config.yaml')).toHaveValue('/missing.txt')
    expect(screen.queryByRole('heading', { name: 'File import job' })).not.toBeInTheDocument()
  })
})
