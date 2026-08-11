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
    await page.getByRole('button', { name: '查看评论：new dynamic content' }).click()
    await expect(page.getByText('暂无评论')).toBeVisible()
    await page.getByRole('button', { name: '收起', exact: true }).click()
    await assertAccessible(page)
    await expect(page).toHaveScreenshot('history-phone.png', {
      animations: 'disabled',
      fullPage: true,
      maxDiffPixels: 1_200,
    })
    expect((await harness.state()).unexpected || []).toEqual([])
  })
})
