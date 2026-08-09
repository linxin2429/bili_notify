import { createContext, useContext, useEffect, useState } from 'react'
import type { ThemePreference } from '../api/types'

const ThemeContext = createContext<{ preference: ThemePreference; setPreference: (value: ThemePreference) => void } | null>(null)
export function ThemeProvider({ children }: { children: React.ReactNode }) {
  const [preference, setState] = useState<ThemePreference>(() => {
    const value = localStorage.getItem('theme')
    return value === 'light' || value === 'dark' ? value : 'system'
  })
  useEffect(() => {
    const media = matchMedia('(prefers-color-scheme: dark)')
    const apply = () => { document.documentElement.dataset.theme = preference === 'system' ? (media.matches ? 'dark' : 'light') : preference }
    apply(); media.addEventListener('change', apply); return () => media.removeEventListener('change', apply)
  }, [preference])
  const setPreference = (value: ThemePreference) => { localStorage.setItem('theme', value); setState(value) }
  return <ThemeContext value={{ preference, setPreference }}>{children}</ThemeContext>
}
export function useThemePreference() {
  const value = useContext(ThemeContext)
  if (!value) throw new Error('useThemePreference 必须位于 ThemeProvider 中')
  return value
}
