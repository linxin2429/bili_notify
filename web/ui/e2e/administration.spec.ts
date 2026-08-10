import { expect, test } from './fixtures'
import {
  ADMIN_PASSWORD,
  REPLACEMENT_PASSWORD,
  assertAccessible,
  createFollowedUP,
  createNotificationChannel,
  initializeAdministrator,
  loginAdministrator,
  navigateTo,
} from './helpers'

test('runs resource administration, audit, and session security workflows', async ({ page, browser, harness }) => {
  await test.step('initialize isolated administration state', async () => {
    await initializeAdministrator(page, harness)
    await createNotificationChannel(page, harness, false)
    await createFollowedUP(page)
  })

  await test.step('edit resources and persist runtime settings', async () => {
    await navigateTo(page, 'UP 主')
    await page.getByRole('button', { name: '编辑' }).click()
    await page.getByLabel('备注名').fill('E2E UP 已修改')
    await page.getByRole('button', { name: '保存' }).click()
    await expect(page.getByText('E2E UP 已修改')).toBeVisible()

    await navigateTo(page, '通知渠道')
    await page.getByRole('button', { name: '编辑' }).click()
    await page.getByLabel('渠道名称').fill('E2E 企业微信 已修改')
    const updateResponse = page.waitForResponse(response => response.url().includes('/api/v2/channels/') && response.request().method() === 'PUT')
    await page.getByRole('button', { name: '保存' }).click()
    expect(JSON.stringify(await (await updateResponse).json())).not.toContain(harness.manifest.webhook_url)
    await expect(page.getByText('E2E 企业微信 已修改')).toBeVisible()

    await navigateTo(page, '设置')
    await page.getByLabel('轮询间隔（秒）').fill('45')
    await page.getByText('日志与保留', { exact: true }).click()
    await page.getByLabel('日志级别').selectOption('debug')
    const settingsResponse = page.waitForResponse(response => response.url().endsWith('/api/v2/settings') && response.request().method() === 'PUT')
    await page.getByRole('button', { name: '保存运行设置' }).click()
    expect((await (await settingsResponse).json()).poll_interval_sec).toBe(45)

    await page.reload({ waitUntil: 'domcontentloaded' })
    await expect(page.getByText('实时', { exact: true }).first()).toBeVisible()
    await navigateTo(page, '设置')
    await expect(page.getByLabel('轮询间隔（秒）')).toHaveValue('45')
    await page.getByText('日志与保留', { exact: true }).click()
    await expect(page.getByLabel('日志级别')).toHaveValue('debug')
  })

  await test.step('expose safe audit evidence for real mutations', async () => {
    await navigateTo(page, '操作日志')
    const auditResponse = page.waitForResponse(response => response.url().includes('/api/v2/audit-logs?action=settings.update'))
    await page.getByLabel('操作').selectOption('settings.update')
    await auditResponse
    await expect(page.getByRole('button', { name: '查看详情' })).toHaveCount(1)
    await page.getByRole('button', { name: '查看详情' }).click()
    const dialog = page.getByRole('dialog', { name: '安全变更摘要' })
    await expect(dialog).toBeVisible()
    expect(await page.locator('main').innerText()).not.toContain(harness.manifest.webhook_url)
    await assertAccessible(page)
    await dialog.getByRole('button', { name: '关闭', exact: true }).click()
  })

  await test.step('replace the current session and invalidate other sessions after a password change', async () => {
    const secondContext = await browser.newContext({ ignoreHTTPSErrors: true, locale: 'zh-CN' })
    const secondPage = await secondContext.newPage()
    await secondPage.goto(harness.manifest.admin_url)
    await loginAdministrator(secondPage)

    await navigateTo(page, '设置')
    await page.getByText('修改管理员密码', { exact: true }).click()
    await page.getByLabel('当前密码').fill(ADMIN_PASSWORD)
    await page.getByLabel('新密码', { exact: true }).fill(REPLACEMENT_PASSWORD)
    await page.getByLabel('确认新密码').fill(REPLACEMENT_PASSWORD)
    await page.getByRole('button', { name: '修改密码' }).click()
    await expect(page.getByText('管理员密码已修改，其他设备的会话已失效')).toBeVisible()
    await expect(page.getByText('修改管理员密码', { exact: true })).toBeVisible()
    await expect(secondPage.getByLabel('管理员密码')).toBeVisible()

    await secondPage.getByLabel('管理员密码').fill(ADMIN_PASSWORD)
    await secondPage.getByRole('button', { name: '登录', exact: true }).click()
    await expect(secondPage.getByText('invalid credentials')).toBeVisible()
    await secondPage.getByLabel('管理员密码').fill(REPLACEMENT_PASSWORD)
    await secondPage.getByRole('button', { name: '登录', exact: true }).click()
    await expect(secondPage.getByText('实时', { exact: true }).first()).toBeVisible()
    await secondContext.close()
  })

  await test.step('delete managed resources through the real API', async () => {
    await navigateTo(page, '通知渠道')
    await page.getByRole('button', { name: '删除' }).click()
    await page.getByRole('button', { name: '确认删除' }).click()
    await expect(page.getByText('尚未配置通知渠道')).toBeVisible()

    await navigateTo(page, 'UP 主')
    await page.getByRole('button', { name: '删除' }).click()
    await page.getByRole('button', { name: '确认删除' }).click()
    await expect(page.getByText('尚未添加 UP 主')).toBeVisible()
    expect((await harness.state()).unexpected || []).toEqual([])
  })
})
