import { QueryClientProvider, useQuery, useQueryClient } from '@tanstack/react-query'
import { useCallback, useEffect } from 'react'
import { RouterProvider } from 'react-router-dom'
import { AuthScreen, SessionProvider } from './modules/session'
import { appRouter } from './app/router'
import { ThemeProvider } from './shared/ui/theme'
import { createQueryClient } from './shared/api/query-client'
import { queryKeys } from './shared/api/query-keys'
import { sessionQuery } from './shared/api/session'
import { setAuthenticationLostHandler } from './shared/api/client'
import { RealtimeSync } from './shared/realtime/RealtimeSync'
import { Alert, Button, LoadingState, NotificationProvider, useNotify } from './shared/ui'

const queryClient = createQueryClient()

export default function App() {
  return <QueryClientProvider client={queryClient}><ThemeProvider><NotificationProvider><SessionBoundary /></NotificationProvider></ThemeProvider></QueryClientProvider>
}

function SessionBoundary() {
  const session = useQuery(sessionQuery())
  const client = useQueryClient()
  const notify = useNotify()
  const loseAuthentication = useCallback(() => {
    client.removeQueries({ predicate: query => query.queryKey[0] !== 'session' })
    client.setQueryData(queryKeys.session, { setup_required: false, authenticated: false })
  }, [client])
  const protocolError = useCallback((message: string) => notify(message, 'danger'), [notify])

  useEffect(() => { setAuthenticationLostHandler(loseAuthentication); return () => setAuthenticationLostHandler(undefined) }, [loseAuthentication])
  if (session.isPending) return <LoadingState label="正在连接 Bili Notify" />
  if (session.isError) return <main className="bootstrap"><Alert tone="danger"><h1>无法连接管理服务</h1><p>{session.error.message}</p><Button variant="primary" onPress={() => void session.refetch()}>重新连接</Button></Alert></main>
  if (!session.data.authenticated || !session.data.csrf_token) return <AuthScreen setup={session.data.setup_required} />
  return <SessionProvider value={{ csrf: session.data.csrf_token }}><RealtimeSync onAuthenticationLost={loseAuthentication} onProtocolError={protocolError}><RouterProvider router={appRouter} /></RealtimeSync></SessionProvider>
}
