import { useQueryClient } from '@tanstack/react-query'
import { createContext, useContext, useEffect, useState } from 'react'
import type { ConnectionState, RealtimeTopic } from '../api/types'
import { sessionAPI } from '../api/session'
import { invalidateTopics, queryKeys } from '../api/query-keys'

const ConnectionContext = createContext<ConnectionState>('connecting')
export function useConnectionState() { return useContext(ConnectionContext) }

export function RealtimeSync({ children, onAuthenticationLost, onProtocolError }: { children: React.ReactNode; onAuthenticationLost: () => void; onProtocolError: (message: string) => void }) {
  const queryClient = useQueryClient()
  const [connection, setConnection] = useState<ConnectionState>('connecting')
  useEffect(() => {
    let stopped = false
    let socket: WebSocket | undefined
    let timer = 0
    let retry = 0
    let revision = -1
    let live = false
    const transition = (state: ConnectionState) => { live = state === 'live'; setConnection(state) }

    const schedule = () => {
      if (stopped) return
      transition('polling')
      const delay = Math.min(30_000, 1_000 * 2 ** retry++)
      timer = window.setTimeout(connect, delay)
    }
    const connect = () => {
      if (stopped) return
      transition(retry ? 'reconnecting' : 'connecting')
      const protocol = location.protocol === 'https:' ? 'wss:' : 'ws:'
      socket = new WebSocket(`${protocol}//${location.host}/api/v2/ws`)
      socket.onopen = () => { retry = 0; transition('live') }
      socket.onmessage = event => {
        const envelope = parseEnvelope(safeJSON(event.data))
        if (!envelope) {
          onProtocolError('实时消息不符合 API 契约，已切换为 REST 重新同步')
          socket?.close(1002, 'invalid protocol')
          return
        }
        if (envelope.revision < revision) return
        revision = envelope.revision
        invalidateTopics(queryClient, envelope.topics)
      }
      socket.onerror = () => transition('polling')
      socket.onclose = async () => {
        if (stopped) return
        transition('polling')
        try {
          const session = await sessionAPI.get()
          queryClient.setQueryData(queryKeys.session, session)
          if (!session.authenticated) { onAuthenticationLost(); return }
        } catch { /* a restart is indistinguishable from a temporary network loss */ }
        schedule()
      }
    }
    connect()
    const poll = window.setInterval(() => {
      if (!live) void queryClient.refetchQueries({ queryKey: queryKeys.runtime, type: 'active' })
    }, 10_000)
    return () => { stopped = true; window.clearTimeout(timer); window.clearInterval(poll); socket?.close(1000, 'client closed') }
  }, [queryClient, onAuthenticationLost, onProtocolError])
  return <ConnectionContext value={connection}>{children}</ConnectionContext>
}

function safeJSON(raw: unknown): unknown {
  try { return JSON.parse(String(raw)) } catch { return null }
}

const topicSet = new Set<RealtimeTopic>(['runtime', 'settings', 'ups', 'channels', 'deliveries', 'bilibili-login', 'microsoft-logins', 'dynamics', 'comments', 'audit-logs'])
export function parseEnvelope(value: unknown): { event: 'sync.required' | 'resources.invalidated'; revision: number; topics: RealtimeTopic[] } | null {
  if (!value || typeof value !== 'object') return null
  const data = value as Record<string, unknown>
  if (Object.keys(data).some(key => !['event', 'revision', 'topics'].includes(key)) || (data.event !== 'sync.required' && data.event !== 'resources.invalidated') || !Number.isSafeInteger(data.revision) || (data.revision as number) < 0 || !Array.isArray(data.topics) || !data.topics.every(topic => typeof topic === 'string' && topicSet.has(topic as RealtimeTopic))) return null
  return { event: data.event, revision: data.revision as number, topics: data.topics as RealtimeTopic[] }
}
