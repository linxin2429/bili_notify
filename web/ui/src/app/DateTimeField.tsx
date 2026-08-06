import { useRef } from 'react'
import { TextField, type TextFieldProps } from '@mui/material'

type DateTimeFieldProps = Omit<TextFieldProps, 'type' | 'onClick'> & {
  value: string
  onChange: (event: React.ChangeEvent<HTMLInputElement>) => void
}

/**
 * datetime-local field that opens the browser picker when the whole control is clicked,
 * not only the calendar icon. Falls back to focusing the input when showPicker is unavailable.
 */
export function DateTimeField({ value, onChange, InputLabelProps, inputProps, sx, ...rest }: DateTimeFieldProps) {
  const inputRef = useRef<HTMLInputElement | null>(null)

  const openPicker = () => {
    const input = inputRef.current
    if (!input) return
    input.focus()
    const withPicker = input as HTMLInputElement & { showPicker?: () => void }
    try {
      withPicker.showPicker?.()
    } catch {
      // Some browsers throw when showPicker is called without a user gesture or when unsupported.
    }
  }

  return <TextField
    {...rest}
    type="datetime-local"
    value={value}
    onChange={onChange}
    inputRef={inputRef}
    onClick={openPicker}
    InputLabelProps={{ shrink: true, ...InputLabelProps }}
    inputProps={{
      ...inputProps,
      onClick: (event: React.MouseEvent<HTMLInputElement>) => {
        // Keep the native icon click path and still open when the text area is clicked.
        event.stopPropagation()
        openPicker()
        inputProps?.onClick?.(event)
      },
    }}
    sx={{
      minWidth: 210,
      '& input': { cursor: 'pointer' },
      ...((typeof sx === 'object' && sx !== null && !Array.isArray(sx)) ? sx : undefined),
    }}
  />
}
