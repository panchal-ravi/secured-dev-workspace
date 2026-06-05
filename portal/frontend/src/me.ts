import { createContext, useContext } from 'react'
import { Me } from './api/client'

// MeContext carries the authenticated user (loaded once in App) so deep
// components like WorkspaceCard can read capabilities (e.g. local_ssh) without
// prop-threading through the pages.
export const MeContext = createContext<Me | null>(null)

export function useMe(): Me | null {
  return useContext(MeContext)
}
