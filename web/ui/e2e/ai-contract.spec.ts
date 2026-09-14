import aiJobPage from '../../testdata/contracts/ai-jobs.json' with { type: 'json' }
import { expect, test } from './fixtures'
import { initializeAdministrator, navigateTo } from './helpers'

test('loads automatic task sources through the strict browser API contract', async ({ page, harness }) => {
  await initializeAdministrator(page, harness)
  await page.route('**/api/v4/ai/jobs?*', route => route.fulfill({ json: aiJobPage }))
  await page.route('**/api/v4/ai/jobs/transcription', route => route.fulfill({ json: aiJobPage.items[0] }))

  await navigateTo(page, 'AI 工作台')
  await expect(page.getByRole('heading', { name: '任务记录' })).toBeVisible()
  await expect(page.getByRole('button', { name: /视频转写.*尝试 0 次/ })).toHaveCount(2)
  await expect(page.getByRole('button', { name: /文本总结.*尝试 0 次/ })).toHaveCount(1)
  const title = page.locator('.ai-job-card__main strong').first()
  await expect.poll(() => title.evaluate(element => element.getBoundingClientRect().height / parseFloat(getComputedStyle(element).lineHeight))).toBeLessThan(1.5)
  if (page.viewportSize()!.width <= 600) {
    const titleBox = await title.boundingBox()
    const progressBox = await page.getByRole('progressbar').first().boundingBox()
    expect(progressBox!.y).toBeGreaterThanOrEqual(titleBox!.y + titleBox!.height)
  }
  await expect(page.getByText('服务器响应不符合 API 契约')).toHaveCount(0)
  await page.getByRole('button', { name: /视频转写.*尝试 0 次/ }).first().click()
  await expect(page.getByRole('button', { name: '取消任务' })).toBeVisible()
  await expect(page.getByText('服务器响应不符合 API 契约')).toHaveCount(0)
})
