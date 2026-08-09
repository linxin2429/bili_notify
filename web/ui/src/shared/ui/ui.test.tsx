import { fireEvent, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { Dialog } from './Dialog'
import {
  Alert, Badge, Button, Card, EmptyState, Icon, IconButton, LoadingState, NativeDateTimeField,
  NotificationProvider, PageError, PageHeader, SelectField, Spinner, SwitchField, TextField, useNotify,
} from './index'
import { ThemeProvider, useThemePreference } from './theme'

describe('shared UI primitives', () => {
  afterEach(() => { vi.useRealTimers(); localStorage.clear() })

  it('connects field values, selection, switches and button states to callbacks', async () => {
    const user = userEvent.setup()
    const changed = vi.fn(); const selected = vi.fn(); const switched = vi.fn(); const pressed = vi.fn()
    render(<>
      <TextField label="名称" value="old" onChange={changed} description="帮助" error="错误" />
      <TextField label="正文" value="body" onChange={changed} multiline disabled />
      <NativeDateTimeField label="时间" value="2026-08-09T12:00" onChange={changed} />
      <SelectField label="选项" value="a" options={[{ value: 'a', label: 'A' }, { value: 'b', label: 'B' }]} onChange={selected} />
      <SwitchField checked={false} onChange={switched}>开关</SwitchField>
      <Button variant="primary" danger className="extra" onPress={pressed}>提交</Button>
      <Button busy>保存</Button><IconButton label="禁用图标" isDisabled>×</IconButton>
    </>)
    await user.clear(screen.getByLabelText(/名称/)); await user.type(screen.getByLabelText(/名称/), 'new')
    await user.selectOptions(screen.getByLabelText('选项'), 'b')
    await user.click(screen.getByLabelText('开关')); await user.click(screen.getByRole('button', { name: '提交' }))
    fireEvent.change(screen.getByLabelText('时间'), { target: { value: '2026-08-10T10:00' } })
    expect(changed).toHaveBeenCalled(); expect(selected).toHaveBeenCalledWith('b'); expect(switched).toHaveBeenCalledWith(true); expect(pressed).toHaveBeenCalledOnce()
    expect(screen.getByRole('button', { name: /处理中/ })).toBeDisabled(); expect(screen.getByLabelText('禁用图标')).toBeDisabled()
  })

  it('renders status, page and presentation primitives with their optional content', async () => {
    const retry = vi.fn(); const user = userEvent.setup()
    render(<><Alert tone="danger">危险</Alert><Badge tone="success">正常</Badge><Card className="extra">卡片</Card><Spinner /><LoadingState label="载入中" /><EmptyState title="空" action={<Button>新增</Button>} /><PageHeader title="标题" subtitle="副标题" action={<Button>操作</Button>} /><PageError error={new Error('断线')} retry={retry} /><PageError error="unknown" /><Icon>i</Icon></>)
    expect(screen.getAllByRole('alert')[0]).toHaveTextContent('危险'); expect(screen.getByText('载入中')).toBeInTheDocument(); expect(screen.getByText('发生未知错误')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: '重试' })); expect(retry).toHaveBeenCalledOnce()
  })

  it('queues, closes and expires notifications', async () => {
    vi.useFakeTimers()
    render(<NotificationProvider><NotifyProbe /></NotificationProvider>)
    fireEvent.click(screen.getByRole('button', { name: '通知' })); expect(screen.getByRole('status')).toHaveTextContent('已保存')
    fireEvent.click(screen.getByLabelText('关闭提示')); expect(screen.queryByText('已保存')).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: '通知' })); await vi.advanceTimersByTimeAsync(6_000); expect(screen.queryByText('已保存')).not.toBeInTheDocument()
  })

  it('opens dialogs, handles backdrop close and renders default and custom actions', async () => {
    const close = vi.fn(); const user = userEvent.setup()
    const { rerender } = render(<Dialog open={false} onClose={close}>隐藏</Dialog>)
    expect(screen.queryByText('隐藏')).not.toBeInTheDocument()
    rerender(<Dialog open title="确认" ariaLabel="确认窗口" fullScreen onClose={close}>正文</Dialog>)
    const dialog = screen.getByRole('dialog', { name: '确认窗口' }); expect(dialog).toHaveAttribute('open'); expect(screen.getByText('确认')).toBeInTheDocument()
    fireEvent.click(dialog); expect(close).toHaveBeenCalledOnce()
    await user.click(screen.getByRole('button', { name: '关闭' })); expect(close).toHaveBeenCalledTimes(2)
    rerender(<Dialog open onClose={close} actions={<Button>自定义</Button>}>正文</Dialog>); expect(screen.getByRole('button', { name: '自定义' })).toBeInTheDocument()
  })
})

describe('theme state', () => {
  afterEach(() => localStorage.clear())

  it('restores, applies and persists preferences including system changes', async () => {
    const listeners = new Set<() => void>(); let dark = false
    vi.stubGlobal('matchMedia', vi.fn(() => ({ get matches() { return dark }, addEventListener: (_name: string, listener: () => void) => listeners.add(listener), removeEventListener: (_name: string, listener: () => void) => listeners.delete(listener) })))
    const user = userEvent.setup(); const view = render(<ThemeProvider><ThemeProbe /></ThemeProvider>)
    expect(document.documentElement.dataset.theme).toBe('light')
    dark = true; listeners.forEach(listener => listener()); expect(document.documentElement.dataset.theme).toBe('dark')
    await user.click(screen.getByRole('button', { name: '设为亮色' })); expect(localStorage.getItem('theme')).toBe('light'); expect(document.documentElement.dataset.theme).toBe('light')
    view.unmount(); expect(listeners.size).toBe(0)
  })

  it('restores a stored dark preference and rejects use outside the provider', () => {
    localStorage.setItem('theme', 'dark'); render(<ThemeProvider><ThemeProbe /></ThemeProvider>); expect(screen.getByText('dark')).toBeInTheDocument()
    expect(() => render(<ThemeProbe />)).toThrow('useThemePreference 必须位于 ThemeProvider 中')
  })
})

function NotifyProbe() { const notify = useNotify(); return <Button onPress={() => notify('已保存', 'success')}>通知</Button> }
function ThemeProbe() { const { preference, setPreference } = useThemePreference(); return <><span>{preference}</span><Button onPress={() => setPreference('light')}>设为亮色</Button></> }
