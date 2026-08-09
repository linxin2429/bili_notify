import { expect, test } from './fixtures'
import {
  assertAccessible,
  completeBilibiliLoginAndBaseline,
  createFollowedUP,
  createNotificationChannel,
  initializeAdministrator,
  navigateTo,
  prepareNewDynamicAfterRestart,
} from './helpers'

test('retains the operational workflow on a phone viewport', async ({ page, harness }) => {
  await test.step('prepare an isolated collected dynamic', async () => {
    await initializeAdministrator(page, harness)
    await createNotificationChannel(page, harness, false)
    await createFollowedUP(page)
    await completeBilibiliLoginAndBaseline(page, harness)
    await prepareNewDynamicAfterRestart(page, harness)
  })

  await test.step('validate responsive navigation, filtering, and accessibility', async () => {
    await page.setViewportSize({ width: 390, height: 844 })
    await expect(page.getByRole('navigation', { name: '移动端主导航' })).toBeVisible()
    await navigateTo(page, '历史')
    await expect(page.getByRole('heading', { name: '历史内容' })).toBeVisible()
    await expect(page.getByText('new dynamic content')).toBeVisible()
    const commentsResponse = page.waitForResponse(response => response.url().includes('/api/v2/comments?'))
    await page.getByRole('tab', { name: 'UP 回复' }).click()
    await commentsResponse
    await expect(page.getByText('当前筛选下没有历史记录')).toBeVisible()
    await page.getByRole('tab', { name: '动态' }).click()
    await expect(page.getByText('new dynamic content')).toBeVisible()
    await assertAccessible(page)
    await expect(page).toHaveScreenshot('history-phone.png', { animations: 'disabled', fullPage: true })
    expect((await harness.state()).unexpected || []).toEqual([])
  })
})
