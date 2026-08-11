import { afterEach, describe, expect, it, vi } from 'vitest'
import { knowledgePlanetCaptcha } from './zsxq-captcha'

describe('Knowledge Planet captcha adapter', () => {
  afterEach(() => {
    delete window.__zsxqCaptcha
    delete window.initAliyunCaptcha
    delete window.AliyunCaptchaConfig
    document.querySelectorAll('script[src*="aliyunCaptcha"]').forEach(node => node.remove())
    document.querySelectorAll('#zsxq-captcha-button').forEach(node => node.remove())
  })

  it('loads Aliyun, rejects malformed results and consumes each token once', async () => {
    let options: Record<string, unknown> | undefined
    const captcha = knowledgePlanetCaptcha()
    const first = captcha.verify()
    const script = document.querySelector<HTMLScriptElement>('script[src*="aliyunCaptcha"]')
    expect(script).not.toBeNull()
    expect(window.AliyunCaptchaConfig).toEqual({ region: 'cn', prefix: '1tp9xr' })
    window.initAliyunCaptcha = vi.fn(value => { options = value })
    script?.dispatchEvent(new Event('load'))
    await vi.waitFor(() => expect(window.initAliyunCaptcha).toHaveBeenCalledOnce())
    const refresh = vi.fn()
    expect(options).toMatchObject({
      prefix: '1tp9xr',
      SceneId: '1e933vjj',
      region: 'cn',
      mode: 'embed',
      element: '#zsxq-captcha',
      button: '#zsxq-captcha-button',
      immediate: true,
      slideStyle: { width: 320, height: 40 },
    })
    const getInstance = options?.getInstance as (value: { refresh: () => void }) => void
    getInstance({ refresh })
    expect(document.querySelector('#zsxq-captcha-button')).not.toBeNull()

    const callback = options?.captchaVerifyCallback as (value: unknown) => Promise<{ captchaResult: boolean; bizResult: boolean }>
    await expect(callback('')).resolves.toEqual({ captchaResult: false, bizResult: false })
    await expect(callback('verified-token')).resolves.toEqual({ captchaResult: true, bizResult: true })
    await expect(first).resolves.toBe('verified-token')

    await callback('second-token')
    await expect(captcha.verify()).resolves.toBe('second-token')
    // Consuming a pre-resolved token refreshes the widget so retries get a new challenge.
    expect(refresh).toHaveBeenCalled()
  })

  it('cancels an older pending verification when a new one starts', async () => {
    let callback: ((value: unknown) => Promise<unknown>) | undefined
    window.initAliyunCaptcha = vi.fn(options => { callback = options.captchaVerifyCallback as (value: unknown) => Promise<unknown> })
    const captcha = knowledgePlanetCaptcha()
    const first = captcha.verify()
    await vi.waitFor(() => expect(window.initAliyunCaptcha).toHaveBeenCalledOnce())
    const second = captcha.verify()
    await expect(first).rejects.toThrow('已开始新的滑块验证')
    await callback?.('latest-token')
    await expect(second).resolves.toBe('latest-token')
  })

  it('retries script loading after a CDN failure', async () => {
    const captcha = knowledgePlanetCaptcha()
    const first = captcha.verify()
    const failed = document.querySelector<HTMLScriptElement>('script[src*="aliyunCaptcha"]')
    expect(failed).not.toBeNull()
    failed?.dispatchEvent(new Event('error'))
    await expect(first).rejects.toThrow('阿里云滑块组件加载失败')
    expect(document.querySelector('script[src*="aliyunCaptcha"]')).toBeNull()

    const second = captcha.verify()
    const retry = document.querySelector<HTMLScriptElement>('script[src*="aliyunCaptcha"]')
    expect(retry).not.toBeNull()
    window.initAliyunCaptcha = vi.fn(options => {
      const callback = options.captchaVerifyCallback as (value: unknown) => Promise<unknown>
      void callback('retry-token')
    })
    retry?.dispatchEvent(new Event('load'))
    await expect(second).resolves.toBe('retry-token')
  })
})
