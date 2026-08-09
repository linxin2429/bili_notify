import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { SessionProvider, useSession } from './session'

describe('session context', () => {
  it('provides the in-memory CSRF token and rejects unauthenticated use', () => {
    render(<SessionProvider value={{ csrf: 'csrf' }}><Probe /></SessionProvider>)
    expect(screen.getByText('csrf')).toBeInTheDocument()
    expect(() => render(<Probe />)).toThrow('useSession 必须在已认证会话中使用')
  })
})

function Probe() { return <span>{useSession().csrf}</span> }
