import { createContext, useContext } from 'react'

export const HaulContext = createContext()

export function useHauls() {
  const ctx = useContext(HaulContext)
  if (!ctx) {
    throw new Error('useHauls must be used within HaulProvider')
  }
  return ctx
}
