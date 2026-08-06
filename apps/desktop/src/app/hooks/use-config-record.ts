import { useQuery } from '@tanstack/react-query'

import { getHermesConfigRecord } from '@/xhermes'
import { queryClient, writeCache } from '@/lib/query-client'
import type { HermesConfigRecord } from '@/types/xhermes'

// One shared cache for the whole profile config record (`GET /api/config`).
// Every settings surface (MCP, model, config) reads and writes through this key
// so a save in one shows in the others, and revisiting a tab paints the cache
// instead of blanking on a fresh fetch.
//
// Distinct from session/hooks/use-xhermes-config.ts, which is side-effecting —
// it pushes personality/cwd/voice/… into the session stores for live chat.
export const XHERMES_CONFIG_KEY = ['xhermes-config-record'] as const

// staleTime 0 → serve cache instantly, background-revalidate on every mount.
export const useHermesConfigRecord = () =>
  useQuery({ queryKey: XHERMES_CONFIG_KEY, queryFn: getHermesConfigRecord, staleTime: 0 })

export const setHermesConfigCache = writeCache<HermesConfigRecord>(XHERMES_CONFIG_KEY)

export const invalidateHermesConfig = () => queryClient.invalidateQueries({ queryKey: XHERMES_CONFIG_KEY })
