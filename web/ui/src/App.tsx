import { useCallback, useMemo, useState } from 'react'
import { Box, CircularProgress, CssBaseline, Snackbar, Stack, ThemeProvider, Typography, createTheme, useMediaQuery } from '@mui/material'
import { useEffect } from 'react'
import type { z } from 'zod'
import { httpJSON } from './api'
import { sessionStateSchema } from './contracts'
import { errorMessage } from './presentation'
import type { ThemePreference } from './types'
import { AuthScreen } from './app/AuthScreen'
import { Console } from './app/Console'

type SessionState = z.infer<typeof sessionStateSchema>

function useThemePreference() {
  const [preference, setPreference] = useState<ThemePreference>(() => (window.localStorage.getItem('theme') as ThemePreference) || 'system')
  const systemDark = useMediaQuery('(prefers-color-scheme: dark)')
  const mode = preference === 'system' ? (systemDark ? 'dark' : 'light') : preference
  const theme = useMemo(() => createTheme({ palette: { mode, primary: { main: mode === 'dark' ? '#ff8ab0' : '#c2185b' }, secondary: { main: mode === 'dark' ? '#5ec7f2' : '#0277bd' }, background: mode === 'dark' ? { default: '#101116', paper: '#191b22' } : { default: '#f6f7fb', paper: '#ffffff' } }, shape: { borderRadius: 14 }, typography: { fontFamily: 'Inter, "Noto Sans SC", "Microsoft YaHei", system-ui, sans-serif' }, components: { MuiButton: { styleOverrides: { root: { minHeight: 42, textTransform: 'none', fontWeight: 650 } } }, MuiIconButton: { styleOverrides: { root: { minWidth: 44, minHeight: 44 } } }, MuiCard: { styleOverrides: { root: { border: mode === 'dark' ? '1px solid #2b2e39' : '1px solid #e7e9f1', boxShadow: 'none' } } } } }), [mode])
  const update = (value: ThemePreference) => { window.localStorage.setItem('theme', value); setPreference(value) }
  return { theme, preference, update }
}

export default function App() {
  const { theme, preference, update } = useThemePreference(); const [session, setSession] = useState<SessionState | null>(null); const [message, setMessage] = useState('')
  const refreshSession = useCallback(async () => { try { setSession(await httpJSON('/api/v1/session', sessionStateSchema)) } catch (error) { setMessage(errorMessage(error)) } }, [])
  useEffect(() => { void refreshSession() }, [refreshSession])
  return <ThemeProvider theme={theme}><CssBaseline />{!session ? <LoadingScreen /> : session.authenticated && session.csrf_token ? <Console csrf={session.csrf_token} themePreference={preference} setThemePreference={update} onAuthLost={refreshSession} /> : <AuthScreen setup={session.setup_required} onAuthenticated={state => setSession({ setup_required: false, authenticated: true, csrf_token: state.csrf_token })} />}<Snackbar open={Boolean(message)} autoHideDuration={5000} onClose={() => setMessage('')} message={message} /></ThemeProvider>
}

function LoadingScreen() {
  return <Box minHeight="100vh" display="grid" sx={{ placeItems: 'center' }}><Stack alignItems="center" spacing={2}><CircularProgress /><Typography color="text.secondary">正在连接 Bili Notify</Typography></Stack></Box>
}
