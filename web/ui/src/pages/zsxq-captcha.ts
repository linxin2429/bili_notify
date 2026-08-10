declare global {
  interface Window {
    __zsxqCaptcha?: { verify: () => Promise<string> }
    initAliyunCaptcha?: (options: Record<string, unknown>) => void
  }
}

const scriptURL = 'https://o.alicdn.com/captcha-frontend/aliyunCaptcha/AliyunCaptcha.js'
let loading: Promise<void> | undefined

function loadScript() {
  if (window.initAliyunCaptcha) return Promise.resolve()
  if (loading) return loading
  loading = new Promise<void>((resolve, reject) => {
    const script = document.createElement('script')
    script.src = scriptURL
    script.async = true
    script.addEventListener('load', () => resolve(), { once: true })
    script.addEventListener('error', () => reject(new Error('阿里云滑块组件加载失败')), { once: true })
    document.head.appendChild(script)
  })
  return loading
}

// The prefix and scene are public browser integration identifiers used by the
// Knowledge Planet web login. They authorize no server operation on their own;
// the one-time verification result is still validated by Knowledge Planet.
export function knowledgePlanetCaptcha(): { verify: () => Promise<string> } {
  let initialized = false
  let token = ''
  let resolveToken: ((value: string) => void) | undefined
  let rejectToken: ((reason: Error) => void) | undefined

  const initialize = async () => {
    if (initialized) return
    await loadScript()
    if (!window.initAliyunCaptcha) throw new Error('阿里云滑块组件不可用')
    window.initAliyunCaptcha({
      prefix: '1tp9xr',
      SceneId: '1e933vjj',
      mode: 'embed',
      element: '#zsxq-captcha',
      immediate: true,
      language: 'cn',
      timeout: 3000,
      slideStyle: { width: 300, height: 32 },
      captchaVerifyCallback: async (value: unknown) => {
        if (typeof value !== 'string' || value.length === 0) return { captchaResult: false, bizResult: false }
        token = value
        resolveToken?.(value)
        resolveToken = undefined
        rejectToken = undefined
        return { captchaResult: true, bizResult: true }
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
        return result
      }
      return new Promise<string>((resolve, reject) => {
        rejectToken?.(new Error('已开始新的滑块验证'))
        resolveToken = resolve
        rejectToken = reject
      })
    },
  }
}
