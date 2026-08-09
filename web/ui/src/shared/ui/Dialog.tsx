import { useEffect, useRef } from 'react'
import { useDialog } from 'react-aria/useDialog'
import { Button } from './index'
import styles from './Dialog.module.css'

export function Dialog({ open, onClose, title, children, actions, fullScreen = false, ariaLabel }: { open: boolean; onClose: () => void; title?: string; children: React.ReactNode; actions?: React.ReactNode; fullScreen?: boolean; ariaLabel?: string }) {
  if (!open) return null
  return <NativeDialog onClose={onClose} title={title} actions={actions} fullScreen={fullScreen} ariaLabel={ariaLabel}>{children}</NativeDialog>
}

function NativeDialog({ onClose, title, children, actions, fullScreen, ariaLabel }: { onClose: () => void; title?: string; children: React.ReactNode; actions?: React.ReactNode; fullScreen: boolean; ariaLabel?: string }) {
  const ref = useRef<HTMLDialogElement | null>(null)
  const { dialogProps, titleProps } = useDialog({ 'aria-label': ariaLabel }, ref)
  useEffect(() => { const node = ref.current; if (!node) return; try { if (!node.open) node.showModal() } catch { node.setAttribute('open', '') } return () => { if (node.open && typeof node.close === 'function') node.close() } }, [])
  return <dialog {...dialogProps} ref={ref} className={`${styles.modal}${fullScreen ? ` ${styles.full}` : ''}`} onClose={onClose} onClick={event => { if (event.target === event.currentTarget) onClose() }}><div className={styles.dialog}>{title && <h2 {...titleProps}>{title}</h2>}<div className={styles.body}>{children}</div><div className={styles.actions}>{actions ?? <Button onPress={onClose}>关闭</Button>}</div></div></dialog>
}
