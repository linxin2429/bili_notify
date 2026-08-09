import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { createMemoryRouter, RouterProvider } from 'react-router-dom'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { RouteErrorBoundary } from './RouteErrorBoundary'

describe('RouteErrorBoundary', () => {
  afterEach(() => vi.unstubAllGlobals())

  it.each([
    { name: 'route response', error: new RouteResponseError(404, 'Not Found'), message: '404 Not Found' },
    { name: 'application error', error: new Error('render failed'), message: 'render failed' },
  ])('presents a useful message for a $name', async ({ error, message }) => {
    const reload = vi.fn()
    vi.stubGlobal('location', { reload })
    const router = createMemoryRouter([{
      path: '/',
      loader: () => { throw error },
      element: <span>unreachable</span>,
      errorElement: <RouteErrorBoundary />,
    }], { initialEntries: ['/'] })

    render(<RouterProvider router={router} />)

    expect(await screen.findByRole('heading', { name: '页面无法显示' })).toBeInTheDocument()
    expect(screen.getByText(message)).toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: '重新加载' }))
    expect(reload).toHaveBeenCalledOnce()
  })
})

class RouteResponseError extends Error {
  internal = false
  data = null
  constructor(readonly status: number, readonly statusText: string) { super(statusText) }
}
