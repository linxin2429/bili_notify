import AxeBuilder from '@axe-core/playwright'
import type { Page } from '@playwright/test'
import { expect, type Harness } from './fixtures'

export const ADMIN_PASSWORD = 'correct horse battery staple'
export const REPLACEMENT_PASSWORD = 'replacement horse battery staple'

const routeByLabel: Record<string, string> = {
  '概览': '/overview',
  '采集源': '/sources',
  '通知渠道': '/channels',
  '投递队列': '/deliveries',
  '历史': '/history',
  'AI 工作台': '/ai',
  'AI 设置': '/ai-settings',
  '操作日志': '/audit-logs',
  '设置': '/settings',
  '更多': '/more',
}

export async function initializeAdministrator(page: Page, harness: Harness) {
  await page.goto(harness.manifest.admin_url)
  await assertAccessible(page)
  await page.getByLabel('初始化码').fill(harness.manifest.setup_code)
  await page.getByLabel('设置管理员密码').fill(ADMIN_PASSWORD)
  await page.getByLabel('确认密码').fill(ADMIN_PASSWORD)
  await page.getByRole('button', { name: '初始化并登录' }).click()
  await expect(page.getByRole('heading', { name: '运行概览' })).toBeVisible()
  await expect(page.getByText('实时', { exact: true }).first()).toBeVisible()
  await assertAccessible(page)
}

export async function loginAdministrator(page: Page, password = ADMIN_PASSWORD) {
  await page.getByLabel('管理员密码').fill(password)
  await page.getByRole('button', { name: '登录', exact: true }).click()
  await expect(page.getByText('实时', { exact: true }).first()).toBeVisible()
}

export async function createNotificationChannel(page: Page, harness: Harness, sendTest: boolean) {
  await navigateTo(page, '通知渠道')
  await page.getByRole('button', { name: '添加渠道' }).click()
  await expect(page.getByRole('dialog', { name: '添加通知渠道' })).toBeVisible()
  await page.getByLabel('渠道名称').fill('E2E 企业微信')
  await page.getByLabel('渠道类型').selectOption({ label: '企业微信机器人' })
  await page.getByLabel('Webhook URL').fill(harness.manifest.webhook_url)
  const createResponse = page.waitForResponse(response => response.url().endsWith('/api/v4/channels') && response.request().method() === 'POST')
  await page.getByRole('button', { name: '保存' }).click()
  expect(JSON.stringify(await (await createResponse).json())).not.toContain(harness.manifest.webhook_url)
  await expect(page.getByText('E2E 企业微信')).toBeVisible()
  expect(await page.evaluate(() => JSON.stringify(window.localStorage))).not.toContain('webhook')

  if (sendTest) {
    await page.getByRole('button', { name: '发送测试' }).click()
    await expect.poll(async () => (await harness.state()).messages.length).toBe(1)
    expect(messageText((await harness.state()).messages[0])).toContain('通知渠道配置成功')
  }
}

export async function createFollowedUP(page: Page) {
  await navigateTo(page, '采集源')
  await page.getByRole('button', { name: '添加 B 站采集源', exact: true }).click()
  await page.getByLabel('UID').fill('42')
  await page.getByLabel('来源名称').fill('E2E UP')
  await page.getByRole('button', { name: '保存' }).click()
  await expect(page.getByRole('heading', { name: 'E2E UP', exact: true })).toBeVisible()
}

export async function completeBilibiliLoginAndBaseline(page: Page, harness: Harness) {
  await navigateTo(page, '概览')
  await page.getByRole('button', { name: '生成登录二维码' }).click()
  await expect(page.getByText('已扫码，请确认')).toBeVisible()
  await expect(page.getByText('E2E Account · UID 100').first()).toBeVisible()
  await expect(page.getByText('服务已就绪')).toBeVisible({ timeout: 25_000 })
  await expect.poll(async () => {
    const state = await harness.state()
    return [state.counts.relations || 0, state.counts.feed_initialize || 0, state.counts.space_feed || 0]
  }, { timeout: 25_000 }).toEqual([1, 1, 1])

  await navigateTo(page, '采集源')
  await expect(page.getByText('基线完成')).toBeVisible()
  await navigateTo(page, '历史')
  await expect(page.getByText('baseline content')).toBeVisible()
  await expect(page.getByText('基线', { exact: true })).toBeVisible()
}

export async function prepareNewDynamicAfterRestart(page: Page, harness: Harness) {
  await harness.setFeed(true)
  await harness.restart()
  await page.reload({ waitUntil: 'domcontentloaded' })
  await loginAdministrator(page)
  await expect.poll(async () => (await harness.state()).counts.feed_fetch || 0, { timeout: 25_000 }).toBe(1)
  await navigateTo(page, '历史')
  await expect(page.getByText('new dynamic content')).toBeVisible()
}

export async function assertAccessible(page: Page) {
  await page.evaluate(async () => {
    await document.fonts.ready
    for (const animation of document.getAnimations()) {
      try {
        animation.finish()
      } catch {
        animation.cancel()
      }
    }
    await new Promise<void>(resolve => requestAnimationFrame(() => resolve()))
  })
  const results = await new AxeBuilder({ page }).analyze()
  expect(results.violations, results.violations.map(violation => `${violation.id}: ${violation.help}`).join('\n')).toEqual([])
}

export async function navigateTo(page: Page, name: string) {
  const clickVisible = async (label: string) => {
    const links = page.getByRole('link', { name: label, exact: true })
    const count = await links.count()
    for (let index = 0; index < count; index += 1) {
      const link = links.nth(index)
      if (await link.isVisible()) {
        await link.click()
        return true
      }
    }
    return false
  }

  if (await clickVisible(name)) return

  // Mobile bottom nav only keeps day-to-day destinations; secondary pages live under「更多」.
  if (await clickVisible('更多')) {
    await expect(page.getByRole('heading', { name: '更多' })).toBeVisible()
    if (await clickVisible(name)) return
  }

  // Deterministic fallback: SPA routes are stable and do not depend on shell chrome visibility.
  const path = routeByLabel[name]
  if (path) {
    await page.evaluate(target => {
      window.history.pushState({}, '', target)
      window.dispatchEvent(new PopStateEvent('popstate'))
    }, path)
    return
  }

  throw new Error(`navigation target is unavailable: ${name}`)
}

export function messageText(message: Record<string, unknown>): string {
  return JSON.stringify(message)
}
