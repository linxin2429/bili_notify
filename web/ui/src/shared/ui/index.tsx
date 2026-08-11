import { createContext, useCallback, useContext, useId, useRef, useState } from 'react'

interface ButtonProps { variant?: 'primary' | 'ghost' | 'outline'; danger?: boolean; busy?: boolean; children: React.ReactNode; className?: string; isDisabled?: boolean; onPress?: () => void; type?: 'button' | 'submit' | 'reset'; role?: string; 'aria-selected'?: boolean }
export function Button({ variant = 'ghost', danger = false, busy = false, children, className = '', isDisabled, onPress, type = 'button', ...aria }: ButtonProps) {
  return <button {...aria} type={type} className={`button button--${variant}${danger ? ' button--danger' : ''} ${className}`} disabled={isDisabled || busy} onClick={onPress}>{busy ? <><Spinner />处理中…</> : children}</button>
}

export function IconButton({ label, children, isDisabled, onPress }: { label: string; children: React.ReactNode; isDisabled?: boolean; onPress?: () => void }) {
  return <button type="button" disabled={isDisabled} onClick={onPress} aria-label={label} title={label} className="icon-button">{children}</button>
}

export function TextField({ label, value, onChange, type = 'text', required, description, error, multiline = false, disabled = false, name, autoComplete, inputMode }: {
  label: string; value: string; onChange: (value: string) => void; type?: string; required?: boolean; description?: string; error?: string;
  multiline?: boolean; disabled?: boolean; name?: string; autoComplete?: string; inputMode?: React.HTMLAttributes<HTMLInputElement>['inputMode']
}) {
  const id = useId()
  const descriptionID = description ? `${id}-description` : undefined
  const errorID = error ? `${id}-error` : undefined
  const describedBy = [descriptionID, errorID].filter(Boolean).join(' ') || undefined
  const common = { id, value, required, disabled, name, 'aria-describedby': describedBy, 'aria-invalid': Boolean(error) || undefined }
  return <div className="field"><label htmlFor={id}>{label}</label>
    {multiline ? <textarea {...common} onChange={event => onChange(event.target.value)} /> : <input {...common} onChange={event => onChange(event.target.value)} type={type} autoComplete={autoComplete} inputMode={inputMode} />}
    {description && <span id={descriptionID} className="field__description">{description}</span>}
    {error && <span id={errorID} className="field__error">{error}</span>}
  </div>
}

export function NativeDateTimeField({ label, value, onChange }: { label: string; value: string; onChange: (value: string) => void }) {
  return <label className="field"><span>{label}</span><input type="datetime-local" value={value} onChange={event => onChange(event.target.value)} /></label>
}

export function SelectField({ label, value, options, onChange, disabled = false }: { label: string; value: string; options: Array<{ value: string; label: string }>; onChange: (value: string) => void; disabled?: boolean }) {
  return <label className="field select"><span>{label}</span><select value={value} onChange={event => onChange(event.target.value)} disabled={disabled}>{options.map(option => <option value={option.value} key={option.value}>{option.label}</option>)}</select></label>
}

export function SwitchField({ checked, onChange, children }: { checked: boolean; onChange: (value: boolean) => void; children: React.ReactNode }) {
  return <label className="switch"><input className="switch__input" type="checkbox" checked={checked} onChange={event => onChange(event.target.checked)} /><span className="switch__track"><span /></span>{children}</label>
}

export function Card({ children, className = '' }: { children: React.ReactNode; className?: string }) { return <section className={`card ${className}`}>{children}</section> }
export function Badge({ children, tone = 'neutral' }: { children: React.ReactNode; tone?: 'neutral' | 'success' | 'warning' | 'danger' | 'info' }) { return <span className={`badge badge--${tone}`}>{children}</span> }
export function Alert({ children, tone = 'info' }: { children: React.ReactNode; tone?: 'info' | 'success' | 'warning' | 'danger' }) { return <div role={tone === 'danger' ? 'alert' : 'status'} className={`alert alert--${tone}`}>{children}</div> }
export function Spinner() { return <span className="spinner" aria-hidden="true" /> }
export function LoadingState({ label = '正在加载' }: { label?: string }) { return <div className="page-state"><Spinner /><p>{label}</p></div> }
export function EmptyState({ icon = '○', title, action }: { icon?: React.ReactNode; title: string; action?: React.ReactNode }) { return <div className="empty-state"><span className="empty-state__icon">{icon}</span><h2>{title}</h2>{action}</div> }
export function PageHeader({ title, subtitle, action }: { title: string; subtitle: string; action?: React.ReactNode }) { return <header className="page-header"><div><h1>{title}</h1><p>{subtitle}</p></div>{action}</header> }
export function PageError({ error, retry }: { error: unknown; retry?: () => void }) { return <Alert tone="danger"><strong>加载失败</strong><p>{error instanceof Error ? error.message : '发生未知错误'}</p>{retry && <Button variant="outline" onPress={retry}>重试</Button>}</Alert> }

type ToastTone = 'info' | 'success' | 'danger'
type Toast = { id: number; message: string; tone: ToastTone }
type Notify = (message: string, tone?: ToastTone) => void

const MAX_TOASTS = 3
const TOAST_TIMEOUT_MS = 6_000

const NotificationContext = createContext<Notify>(() => undefined)
export function useNotify() { return useContext(NotificationContext) }

export function NotificationProvider({ children }: { children: React.ReactNode }) {
  const [toasts, setToasts] = useState<Toast[]>([])
  const seq = useRef(0)

  const dismiss = useCallback((id: number) => {
    setToasts(current => current.filter(toast => toast.id !== id))
  }, [])

  const notify: Notify = useCallback((message, tone = 'info') => {
    const id = ++seq.current
    setToasts(current => [...current, { id, message, tone }].slice(-MAX_TOASTS))
    // danger stays until the user dismisses; other tones auto-expire
    if (tone !== 'danger') {
      window.setTimeout(() => setToasts(current => current.filter(toast => toast.id !== id)), TOAST_TIMEOUT_MS)
    }
  }, [])

  return <NotificationContext value={notify}>
    {children}
    {toasts.length > 0 && <div className="toast-stack" style={{ position: 'fixed', zIndex: 100, right: '1rem', bottom: '1rem', display: 'flex', flexDirection: 'column', gap: '0.5rem', maxWidth: 'min(420px, calc(100vw - 2rem))' }}>
      {toasts.map(toast => <div key={toast.id} className={`toast toast--${toast.tone}`} role="status" style={{ position: 'static', maxWidth: 'none' }}>
        <span>{toast.message}</span>
        <IconButton label="关闭提示" onPress={() => dismiss(toast.id)}>×</IconButton>
      </div>)}
    </div>}
  </NotificationContext>
}

export const Icon = ({ children }: { children: React.ReactNode }) => <span className="icon" aria-hidden="true">{children}</span>
