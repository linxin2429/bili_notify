import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import { DateTimeField } from './DateTimeField'

describe('DateTimeField', () => {
  it('opens the native picker when the whole field is clicked', async () => {
    const user = userEvent.setup()
    const onChange = vi.fn()
    render(<DateTimeField label="开始时间" value="" onChange={onChange} />)
    const input = screen.getByLabelText('开始时间') as HTMLInputElement & { showPicker?: () => void }
    const showPicker = vi.fn()
    input.showPicker = showPicker
    await user.click(input)
    expect(showPicker).toHaveBeenCalled()
  })

  it('still reports value changes', async () => {
    const user = userEvent.setup()
    const onChange = vi.fn()
    render(<DateTimeField label="结束时间" value="" onChange={onChange} />)
    await user.type(screen.getByLabelText('结束时间'), '2026-08-06T08:00')
    expect(onChange).toHaveBeenCalled()
  })
})
