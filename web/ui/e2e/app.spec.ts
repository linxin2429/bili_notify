import AxeBuilder from '@axe-core/playwright'
import { expect, request, test, type APIRequestContext, type Page } from '@playwright/test'
import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { spawn, type ChildProcessWithoutNullStreams } from 'node:child_process'

interface HarnessManifest {
  event: 'e2e_ready'
  admin_url: string
  observe_url: string
  webhook_url: string
  control_url: string
  control_token: string
  setup_code: string
}

interface HarnessState {
  counts: Record<string, number>
  messages: Array<Record<string, unknown>>
  unexpected: string[] | null
  new_dynamic: boolean
  webhook_mode: 'success' | 'permanent_failure'
  generation: number
}

let server: ChildProcessWithoutNullStreams
let control: APIRequestContext
let dataDir = ''
let logs = ''
let manifest: HarnessManifest

test.beforeAll(async () => {
  dataDir = mkdtempSync(join(tmpdir(), 'bili-notify-e2e-'))
  const repository = resolve(import.meta.dirname, '../../..')
  const started = await startHarness(repository, dataDir)
  server = started.server
  manifest = started.manifest
  control = await request.newContext({
    baseURL: manifest.control_url,
    ignoreHTTPSErrors: true,
    extraHTTPHeaders: { Authorization: `Bearer ${manifest.control_token}` },
  })
})

test.afterEach(async ({}, testInfo) => {
  if (testInfo.status !== testInfo.expectedStatus) {
    await testInfo.attach('harness.log', { body: logs, contentType: 'text/plain' })
  }
})

test.afterAll(async () => {
  await control?.dispose()
  await stopHarness(server)
  if (dataDir) rmSync(dataDir, { recursive: true, force: true })
})

test('runs the collection, durable outbox, restart, administration, and retry journey', async ({ page, browser }) => {
  await test.step('initialize the administrator and realtime console', async () => {
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
    await page.goto(manifest.admin_url)
    await assertAccessible(page)
    await page.getByLabel('初始化码').fill(manifest.setup_code)
    await page.getByLabel('设置管理员密码').fill('correct horse battery staple')
    await page.getByLabel('确认密码').fill('correct horse battery staple')
    await page.getByRole('button', { name: '初始化并登录' }).click()
    await expect(page.getByRole('heading', { name: '运行概览' })).toBeVisible()
    await expect(page.getByText('实时', { exact: true }).first()).toBeVisible()
    await assertAccessible(page)
  })

  await test.step('create and test an HTTPS notification channel', async () => {
    await navigateTo(page, '通知渠道')
    await page.getByRole('button', { name: '添加渠道' }).click()
    await expect(page.getByRole('dialog', { name: '添加通知渠道' })).toBeVisible()
    await page.getByLabel('渠道名称').fill('E2E 企业微信')
    await page.getByLabel('渠道类型').click()
    await page.getByRole('option', { name: '企业微信机器人' }).click()
    await page.getByLabel('Webhook URL').fill(manifest.webhook_url)
    const createResponse = page.waitForResponse(response => response.url().endsWith('/api/v1/channels') && response.request().method() === 'POST')
    await page.getByRole('button', { name: '保存' }).click()
    expect(JSON.stringify(await (await createResponse).json())).not.toContain(manifest.webhook_url)
    await expect(page.getByText('E2E 企业微信')).toBeVisible()
    expect(await page.evaluate(() => JSON.stringify(window.localStorage))).not.toContain('webhook')
    await page.getByRole('button', { name: '发送测试' }).click()
    await expect.poll(async () => (await harnessState()).messages.length).toBe(1)
    expect(messageText((await harnessState()).messages[0])).toContain('通知渠道配置成功')
  })

  await test.step('add a followed UP and complete the fake QR login', async () => {
    await navigateTo(page, 'UP 主')
    await page.getByRole('button', { name: '添加 UP 主' }).click()
    await page.getByLabel('UID').fill('42')
    await page.getByLabel('备注名').fill('E2E UP')
    await page.getByRole('button', { name: '保存' }).click()
    await expect(page.getByText('E2E UP')).toBeVisible()

    await navigateTo(page, '概览')
    await page.getByRole('button', { name: '生成登录二维码' }).click()
    await expect(page.getByText('已扫码，请确认')).toBeVisible()
    await expect(page.getByText('E2E Account · UID 100')).toBeVisible()
    await expect(page.getByText('服务已就绪')).toBeVisible({ timeout: 25_000 })
  })

  await test.step('establish a space baseline and switch to the aggregate feed', async () => {
    await expect.poll(async () => {
      const state = await harnessState()
      return [state.counts.relations || 0, state.counts.feed_initialize || 0, state.counts.space_feed || 0]
    }, { timeout: 25_000 }).toEqual([1, 1, 1])

    await navigateTo(page, 'UP 主')
    await expect(page.getByText('基线已建立')).toBeVisible()
    await expect(page.getByText('当前账号已关注')).toBeVisible()
    await expect(page.getByText('综合流采集')).toBeVisible()

    await navigateTo(page, '历史')
    await expect(page.getByText('baseline content')).toBeVisible()
    await expect(page.getByText('基线', { exact: true })).toBeVisible()
    expect((await harnessState()).messages).toHaveLength(1)
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
    await setControl('/webhook', { mode: 'permanent_failure' })
    await setControl('/feed', { new_dynamic: true })

    await expect.poll(async () => (await harnessState()).counts.feed_fetch || 0, { timeout: 25_000 }).toBe(1)
    await expect.poll(async () => (await harnessState()).messages.length).toBe(2)
    const state = await harnessState()
    feedUpdatesBeforeRestart = state.counts.feed_update || 0
    expect(messageText(state.messages[1])).toContain('new dynamic content')

    await navigateTo(page, '投递队列')
    await expect(page.getByText('已阻塞', { exact: true }).last()).toBeVisible()
    await expect(page.getByText('new dynamic content')).toBeVisible()
    await navigateTo(page, '历史')
    await expect(page.getByText('new dynamic content')).toBeVisible()
  })

  await test.step('restart with the blocked outbox and preserve exactly-once state', async () => {
    const response = await control.post(`${manifest.control_url}/restart`)
    expect(response.ok()).toBeTruthy()
    await page.reload({ waitUntil: 'domcontentloaded' })
    await page.getByLabel('管理员密码').fill('correct horse battery staple')
    await page.getByRole('button', { name: '登录', exact: true }).click()
    await expect(page.getByText('实时', { exact: true }).first()).toBeVisible()

    await expect.poll(async () => (await harnessState()).generation).toBe(2)
    await expect.poll(async () => (await harnessState()).counts.feed_update || 0).toBeGreaterThan(feedUpdatesBeforeRestart)
    expect((await harnessState()).messages).toHaveLength(2)

    await navigateTo(page, 'UP 主')
    await expect(page.getByText('当前账号已关注')).toBeVisible()
    await expect(page.getByText('综合流采集')).toBeVisible()
    await navigateTo(page, '投递队列')
    await expect(page.getByText('已阻塞', { exact: true }).last()).toBeVisible()
    await expect(page.getByText('共 1 个任务，页面展示前 1 个。')).toBeVisible()
    await expect(page.getByText('已尝试 1 次')).toBeVisible()
  })

  await test.step('retry successfully and expose healthy operational evidence', async () => {
    await setControl('/webhook', { mode: 'success' })
    await page.getByRole('button', { name: '立即重试' }).click()
    await expect.poll(async () => (await harnessState()).messages.length).toBe(3)
    expect(messageText((await harnessState()).messages[2])).toContain('new dynamic content')
    await expect(page.getByText('当前筛选下没有待投递任务')).toBeVisible()

    await expect.poll(async () => (await control.get(`${manifest.observe_url}/readyz`)).status()).toBe(200)
    expect((await control.get(`${manifest.observe_url}/metrics`)).status()).toBe(404)
    expect((await harnessState()).unexpected || []).toEqual([])
  })

  await test.step('persist settings and exercise resource administration', async () => {
    await navigateTo(page, 'UP 主')
    await page.getByRole('button', { name: '编辑' }).click()
    await page.getByLabel('备注名').fill('E2E UP 已修改')
    await page.getByRole('button', { name: '保存' }).click()
    await expect(page.getByText('E2E UP 已修改')).toBeVisible()

    await navigateTo(page, '通知渠道')
    await page.getByRole('button', { name: '编辑' }).click()
    await page.getByLabel('渠道名称').fill('E2E 企业微信 已修改')
    const updateResponse = page.waitForResponse(response => response.url().includes('/api/v1/channels/') && response.request().method() === 'PUT')
    await page.getByRole('button', { name: '保存' }).click()
    expect(JSON.stringify(await (await updateResponse).json())).not.toContain(manifest.webhook_url)
    await expect(page.getByText('E2E 企业微信 已修改')).toBeVisible()

    await navigateTo(page, '设置')
    await page.getByLabel('轮询间隔（秒）').fill('45')
    await page.getByLabel('日志级别').click()
    await page.getByRole('option', { name: 'debug' }).click()
    const settingsResponse = page.waitForResponse(response => response.url().endsWith('/api/v1/settings') && response.request().method() === 'PUT')
    await page.getByRole('button', { name: '保存运行设置' }).click()
    expect((await (await settingsResponse).json()).poll_interval_sec).toBe(45)

    await page.reload({ waitUntil: 'domcontentloaded' })
    await expect(page.getByText('实时', { exact: true }).first()).toBeVisible()
    await navigateTo(page, '设置')
    await expect(page.getByLabel('轮询间隔（秒）')).toHaveValue('45')
    await expect(page.getByLabel('日志级别')).toHaveText('debug')
  })

  await test.step('expose safe audit evidence for the real mutations', async () => {
    await navigateTo(page, '操作日志')
    await page.getByLabel('操作').click()
    const auditResponse = page.waitForResponse(response => response.url().includes('/api/v1/audit-logs?action=settings.update'))
    await page.getByRole('option', { name: '修改采集参数' }).click()
    await auditResponse
    await expect(page.getByRole('button', { name: '查看详情' })).toHaveCount(1)
    await page.getByRole('button', { name: '查看详情' }).click()
    await expect(page.getByText('安全变更摘要')).toBeVisible()
    expect(await page.locator('main').innerText()).not.toContain(manifest.webhook_url)
    await assertAccessible(page)
  })

  await test.step('retain the operational workflow on a phone viewport', async () => {
    await page.setViewportSize({ width: 390, height: 844 })
    await expect(page.getByLabel('打开导航')).toBeVisible()
    await navigateTo(page, '历史')
    await expect(page.getByRole('heading', { name: '历史内容' })).toBeVisible()
    await expect(page.getByText('new dynamic content')).toBeVisible()
    const commentsResponse = page.waitForResponse(response => response.url().includes('/api/v1/comments?'))
    await page.getByRole('tab', { name: 'UP 回复' }).click()
    await commentsResponse
    await expect(page.getByText('当前筛选下没有历史记录')).toBeVisible()
    await page.getByRole('tab', { name: '动态' }).click()
    await expect(page.getByText('new dynamic content')).toBeVisible()
    await assertAccessible(page)
    await expect(page).toHaveScreenshot('history-phone.png', { animations: 'disabled', fullPage: true })
  })

  await test.step('invalidate every old session after a password change', async () => {
    const secondContext = await browser.newContext({ ignoreHTTPSErrors: true, locale: 'zh-CN' })
    const secondPage = await secondContext.newPage()
    await secondPage.goto(manifest.admin_url)
    await secondPage.getByLabel('管理员密码').fill('correct horse battery staple')
    await secondPage.getByRole('button', { name: '登录', exact: true }).click()
    await expect(secondPage.getByText('实时', { exact: true }).first()).toBeVisible()

    await page.setViewportSize({ width: 1440, height: 1000 })
    await navigateTo(page, '设置')
    await page.getByLabel('当前密码').fill('correct horse battery staple')
    await page.getByLabel('新密码', { exact: true }).fill('replacement horse battery staple')
    await page.getByLabel('确认新密码').fill('replacement horse battery staple')
    await page.getByRole('button', { name: '修改密码' }).click()
    await expect(page.getByText('实时', { exact: true }).first()).toBeVisible()
    await expect(secondPage.getByLabel('管理员密码')).toBeVisible()

    await secondPage.getByLabel('管理员密码').fill('correct horse battery staple')
    await secondPage.getByRole('button', { name: '登录', exact: true }).click()
    await expect(secondPage.getByText('invalid credentials')).toBeVisible()
    await secondPage.getByLabel('管理员密码').fill('replacement horse battery staple')
    await secondPage.getByRole('button', { name: '登录', exact: true }).click()
    await expect(secondPage.getByText('实时', { exact: true }).first()).toBeVisible()
    await secondContext.close()
  })

  await test.step('delete managed resources through the real API', async () => {
    await navigateTo(page, '通知渠道')
    page.once('dialog', dialog => dialog.accept())
    await page.getByRole('button', { name: '删除' }).click()
    await expect(page.getByText('尚未配置通知渠道')).toBeVisible()

    await navigateTo(page, 'UP 主')
    page.once('dialog', dialog => dialog.accept())
    await page.getByRole('button', { name: '删除' }).click()
    await expect(page.getByText('尚未添加 UP 主')).toBeVisible()
  })
})

async function assertAccessible(page: Page) {
  const results = await new AxeBuilder({ page }).analyze()
  expect(results.violations, results.violations.map(violation => `${violation.id}: ${violation.help}`).join('\n')).toEqual([])
}

async function navigateTo(page: Page, name: string) {
  const buttons = page.getByRole('button', { name, exact: true })
  const opener = page.getByLabel('打开导航')
  if (await buttons.count() === 0 && await opener.isVisible()) await opener.click()
  else await expect.poll(() => buttons.count()).toBeGreaterThan(0)
  for (let index = 0; index < await buttons.count(); index += 1) {
    const button = buttons.nth(index)
    if (await button.isVisible()) {
      await button.click()
      return
    }
  }
  await opener.click()
  await expect.poll(async () => {
    for (let index = 0; index < await buttons.count(); index += 1) if (await buttons.nth(index).isVisible()) return true
    return false
  }).toBe(true)
  for (let index = 0; index < await buttons.count(); index += 1) {
    const button = buttons.nth(index)
    if (await button.isVisible()) {
      await button.click()
      return
    }
  }
  throw new Error(`navigation target is unavailable: ${name}`)
}

async function harnessState(): Promise<HarnessState> {
  const response = await control.get(`${manifest.control_url}/state`)
  expect(response.ok()).toBeTruthy()
  return response.json() as Promise<HarnessState>
}

async function setControl(path: string, data: unknown) {
  const response = await control.put(`${manifest.control_url}${path}`, { data })
  expect(response.status()).toBe(204)
}

function messageText(message: Record<string, unknown>): string {
  return JSON.stringify(message)
}

async function startHarness(repository: string, directory: string) {
  const child = spawn('go', ['run', '-trimpath', './e2e/harness', '--data-dir', directory], {
    cwd: repository,
    env: { ...process.env, CGO_ENABLED: '0' },
  })
  const ready = new Promise<HarnessManifest>((resolveReady, reject) => {
    let stdout = ''
    const timer = setTimeout(() => reject(new Error(`harness startup timed out:\n${logs}`)), 30_000)
    const consume = (chunk: Buffer, source: 'stdout' | 'stderr') => {
      const text = chunk.toString()
      logs += text
      if (source !== 'stdout') return
      stdout += text
      const lines = stdout.split('\n')
      stdout = lines.pop() || ''
      for (const line of lines) {
        try {
          const value = JSON.parse(line) as Partial<HarnessManifest>
          if (value.event === 'e2e_ready') {
            clearTimeout(timer)
            resolveReady(value as HarnessManifest)
          }
        } catch {
          // Non-manifest output remains available in the failure attachment.
        }
      }
    }
    child.stdout.on('data', chunk => consume(chunk, 'stdout'))
    child.stderr.on('data', chunk => consume(chunk, 'stderr'))
    child.once('exit', code => {
      clearTimeout(timer)
      reject(new Error(`harness exited with ${code}:\n${logs}`))
    })
  })
  return { server: child, manifest: await ready }
}

async function stopHarness(child?: ChildProcessWithoutNullStreams) {
  if (!child || child.exitCode !== null) return
  const exited = new Promise<void>(resolveExit => child.once('exit', () => resolveExit()))
  child.kill('SIGTERM')
  await Promise.race([
    exited,
    new Promise<void>((_, reject) => setTimeout(() => reject(new Error('harness shutdown timed out')), 25_000)),
  ])
}
