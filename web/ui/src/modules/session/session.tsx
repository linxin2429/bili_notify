import { createContext, useContext } from 'react'

const SessionContext = createContext<{ csrf: string } | null>(null)
export const SessionProvider = SessionContext.Provider
export function useSession() {
  const session = useContext(SessionContext)
  if (!session) throw new Error('useSession 必须在已认证会话中使用')
  return session
}
