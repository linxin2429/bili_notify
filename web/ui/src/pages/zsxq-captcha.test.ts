import { afterEach, describe, expect, it, vi } from 'vitest'
import { knowledgePlanetCaptcha } from './zsxq-captcha'

describe('Knowledge Planet captcha adapter', () => {
  afterEach(() => {
    delete window.__zsxqCaptcha
    delete window.initAliyunCaptcha
    document.querySelectorAll('script[src*="aliyunCaptcha"]').forEach(node => node.remove())
  })

  it('loads Aliyun, rejects malformed results and consumes each token once', async () => {
    let options: Record<string, unknown> | undefined
    const captcha = knowledgePlanetCaptcha()
    const first = captcha.verify()
    const script = document.querySelector<HTMLScriptElement>('script[src*="aliyunCaptcha"]')
    expect(script).not.toBeNull()
    window.initAliyunCaptcha = vi.fn(value => { options = value })
    script?.dispatchEvent(new Event('load'))
    await vi.waitFor(() => expect(window.initAliyunCaptcha).toHaveBeenCalledOnce())

    const callback = options?.captchaVerifyCallback as (value: unknown) => Promise<{ captchaResult: boolean; bizResult: boolean }>
    await expect(callback('')).resolves.toEqual({ captchaResult: false, bizResult: false })
    await expect(callback('verified-token')).resolves.toEqual({ captchaResult: true, bizResult: true })
    await expect(first).resolves.toBe('verified-token')

    await callback('second-token')
    await expect(captcha.verify()).resolves.toBe('second-token')
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
})
