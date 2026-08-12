import { useQueryClient } from '@tanstack/react-query'
import { createContext, useContext, useEffect, useState } from 'react'
import { parseRealtimeEnvelope } from '../api/realtime-contract'
import type { ConnectionState } from '../api/types'
import { sessionAPI } from '../api/session'
import { isSessionReplacementPending } from '../api/session-cache'
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
      socket = new WebSocket(`${protocol}//${location.host}/api/v4/ws`)
      socket.onopen = () => { retry = 0; transition('live') }
      socket.onmessage = event => {
        const envelope = parseEnvelope(safeJSON(event.data))
        if (!envelope) {
          onProtocolError('实时消息不符合 API 契约，已切换为 REST 重新同步')
          socket?.close(4002, 'invalid application message')
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
        const replacementWasPending = isSessionReplacementPending()
        try {
          const session = await sessionAPI.get()
          if (stopped) return
          if (!session.authenticated && (replacementWasPending || isSessionReplacementPending())) { schedule(); return }
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

export function parseEnvelope(value: unknown) {
  return parseRealtimeEnvelope(value)
}
