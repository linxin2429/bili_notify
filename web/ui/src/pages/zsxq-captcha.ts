declare global {
  interface Window {
    __zsxqCaptcha?: { verify: () => Promise<string> }
    initAliyunCaptcha?: (options: Record<string, unknown>) => void
    AliyunCaptchaConfig?: { region: string; prefix: string }
  }
}

const scriptURL = 'https://o.alicdn.com/captcha-frontend/aliyunCaptcha/AliyunCaptcha.js'
// Public browser integration identifiers used by Knowledge Planet web login.
// They authorize no server operation on their own; Knowledge Planet still
// validates the one-time captcha result when sending the SMS code.
const captchaPrefix = '1tp9xr'
const captchaSceneId = '1e933vjj'
const captchaElement = '#zsxq-captcha'
const captchaButton = '#zsxq-captcha-button'
let loading: Promise<void> | undefined

type CaptchaInstance = { refresh?: () => void; show?: () => void }

function ensureTriggerButton() {
  if (document.querySelector(captchaButton)) return
  const button = document.createElement('button')
  button.id = captchaButton.slice(1)
  button.type = 'button'
  button.hidden = true
  button.tabIndex = -1
  button.setAttribute('aria-hidden', 'true')
  document.body.appendChild(button)
}

function loadScript(): Promise<void> {
  if (window.initAliyunCaptcha) return Promise.resolve()
  if (loading) return loading
  // Region/prefix must exist before the SDK boots; otherwise device fingerprint
  // endpoints are not selected and verify tokens are rejected upstream.
  window.AliyunCaptchaConfig = { region: 'cn', prefix: captchaPrefix }
  loading = new Promise<void>((resolve, reject) => {
    const script = document.createElement('script')
    script.src = scriptURL
    script.async = true
    const fail = () => {
      // Drop the failed promise and script node so a later click can retry the CDN load.
      loading = undefined
      script.remove()
      reject(new Error('阿里云滑块组件加载失败'))
    }
    script.addEventListener('load', () => {
      if (!window.initAliyunCaptcha) {
        fail()
        return
      }
      // Clear after success so a later session without initAliyunCaptcha re-injects
      // instead of reusing a stale resolved promise (e.g. test teardown).
      loading = undefined
      resolve()
    }, { once: true })
    script.addEventListener('error', fail, { once: true })
    document.head.appendChild(script)
  })
  return loading
}

export function knowledgePlanetCaptcha(): { verify: () => Promise<string> } {
  let initialized = false
  let instance: CaptchaInstance | undefined
  let token = ''
  let resolveToken: ((value: string) => void) | undefined
  let rejectToken: ((reason: Error) => void) | undefined

  const initialize = async () => {
    if (initialized) return
    ensureTriggerButton()
    await loadScript()
    if (!window.initAliyunCaptcha) throw new Error('阿里云滑块组件不可用')
    window.initAliyunCaptcha({
      prefix: captchaPrefix,
      SceneId: captchaSceneId,
      region: 'cn',
      mode: 'embed',
      element: captchaElement,
      // Official SDK requires a button selector even when the business trigger is separate.
      button: captchaButton,
      immediate: true,
      language: 'cn',
      timeout: 10000,
      // Aliyun documents a minimum slide width of 320px; narrower values break the icon layout.
      slideStyle: { width: 320, height: 40 },
      getInstance: (value: CaptchaInstance) => { instance = value },
      captchaVerifyCallback: async (value: unknown) => {
        if (typeof value !== 'string' || value.length === 0) return { captchaResult: false, bizResult: false }
        token = value
        resolveToken?.(value)
        resolveToken = undefined
        rejectToken = undefined
        // Knowledge Planet re-validates the one-time token server-side. Returning
        // captchaResult:true only advances the client widget after a complete slide.
        return { captchaResult: true, bizResult: true }
      },
      onBizResultCallback: () => undefined,
      onError: (error: unknown) => {
        const message = error instanceof Error ? error.message : '滑块验证失败，请重试'
        rejectToken?.(new Error(message))
        resolveToken = undefined
        rejectToken = undefined
      },
    })
    initialized = true
  }

  return {
    verify: async () => {
      await initialize()
      if (token) {
        const result = token
        token = ''
        // Force a fresh challenge after this token is consumed so retries do not
        // reuse a one-time captcha result that Knowledge Planet already saw.
        try { instance?.refresh?.() } catch { /* ignore SDK refresh failures */ }
        return result
      }
      try { instance?.refresh?.() } catch { /* first challenge may not need refresh */ }
      return new Promise<string>((resolve, reject) => {
        rejectToken?.(new Error('已开始新的滑块验证'))
        resolveToken = resolve
        rejectToken = reject
      })
    },
  }
}
