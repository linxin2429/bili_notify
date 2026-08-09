import { expect, test } from './fixtures'
import {
  completeBilibiliLoginAndBaseline,
  createFollowedUP,
  createNotificationChannel,
  initializeAdministrator,
  loginAdministrator,
  messageText,
  navigateTo,
} from './helpers'

test('runs the collection, durable outbox, restart, and retry journey', async ({ page, harness }) => {
  await page.addInitScript(() => {
    const NativeWebSocket = window.WebSocket
    const sockets: WebSocket[] = []
    class TrackedWebSocket extends NativeWebSocket {
      constructor(url: string | URL, protocols?: string | string[]) {
        super(url, protocols)
        sockets.push(this)
      }
    }
    Object.defineProperty(window, 'WebSocket', { value: TrackedWebSocket })
    Object.defineProperty(window, '__e2eSockets', { value: sockets })
  })

  await test.step('initialize the administrator and realtime console', async () => {
    await initializeAdministrator(page, harness)
  })

  await test.step('create and test an HTTPS notification channel', async () => {
    await createNotificationChannel(page, harness, true)
  })

  await test.step('add a followed UP and establish the collection baseline', async () => {
    await createFollowedUP(page)
    await completeBilibiliLoginAndBaseline(page, harness)
    expect((await harness.state()).messages).toHaveLength(1)
  })

  await test.step('recover the realtime console after a network interruption without reloading', async () => {
    const socketCount = await page.evaluate(() => (window as typeof window & { __e2eSockets: WebSocket[] }).__e2eSockets.length)
    await page.evaluate(() => (window as typeof window & { __e2eSockets: WebSocket[] }).__e2eSockets.at(-1)?.close(4001, 'e2e interruption'))
    await expect(page.getByText(/实时连接已中断/)).toBeVisible()
    await expect(page.getByText('实时', { exact: true }).first()).toBeVisible({ timeout: 10_000 })
    await expect(page.getByText(/实时连接已中断/)).not.toBeVisible()
    await expect.poll(() => page.evaluate(() => (window as typeof window & { __e2eSockets: WebSocket[] }).__e2eSockets.length)).toBeGreaterThan(socketCount)
  })

  let feedUpdatesBeforeRestart = 0
  await test.step('archive a new dynamic and block its failed delivery', async () => {
    await harness.setWebhook('permanent_failure')
    await harness.setFeed(true)

    await expect.poll(async () => (await harness.state()).counts.feed_fetch || 0, { timeout: 25_000 }).toBe(1)
    await expect.poll(async () => (await harness.state()).messages.length).toBe(2)
    const state = await harness.state()
    feedUpdatesBeforeRestart = state.counts.feed_update || 0
    expect(messageText(state.messages[1])).toContain('new dynamic content')

    await navigateTo(page, '投递队列')
    await expect(page.getByText('已阻塞', { exact: true }).last()).toBeVisible()
    await expect(page.getByText('new dynamic content')).toBeVisible()
    await navigateTo(page, '历史')
    await expect(page.getByText('new dynamic content')).toBeVisible()
  })

  await test.step('restart with the blocked outbox and preserve exactly-once state', async () => {
    await harness.restart()
    await page.reload({ waitUntil: 'domcontentloaded' })
    await loginAdministrator(page)

    await expect.poll(async () => (await harness.state()).generation).toBe(2)
    await expect.poll(async () => (await harness.state()).counts.feed_update || 0).toBeGreaterThan(feedUpdatesBeforeRestart)
    expect((await harness.state()).messages).toHaveLength(2)

    await navigateTo(page, 'UP 主')
    await expect(page.getByText('当前账号已关注')).toBeVisible()
    await expect(page.getByText('综合流采集')).toBeVisible()
    await navigateTo(page, '投递队列')
    await expect(page.getByText('已阻塞', { exact: true }).last()).toBeVisible()
    await expect(page.getByText('共 1 个任务，页面展示前 1 个。')).toBeVisible()
    await expect(page.getByText('已尝试 1 次')).toBeVisible()
  })

  await test.step('retry successfully and expose healthy operational evidence', async () => {
    await harness.setWebhook('success')
    await page.getByRole('button', { name: '立即重试' }).click()
    await expect.poll(async () => (await harness.state()).messages.length).toBe(3)
    expect(messageText((await harness.state()).messages[2])).toContain('new dynamic content')
    await expect(page.getByText('当前筛选下没有待投递任务')).toBeVisible()

    await expect.poll(async () => (await page.request.get(`${harness.manifest.observe_url}/readyz`)).status()).toBe(200)
    expect((await page.request.get(`${harness.manifest.observe_url}/metrics`)).status()).toBe(404)
    expect((await harness.state()).unexpected || []).toEqual([])
  })
})
