import { expect, test } from '@playwright/test'
import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { spawn, type ChildProcessWithoutNullStreams } from 'node:child_process'

let server: ChildProcessWithoutNullStreams
let dataDir: string
let setupCode = ''

test.beforeAll(async () => {
  dataDir = mkdtempSync(join(tmpdir(), 'bili-notify-e2e-'))
  const repository = resolve(import.meta.dirname, '../../..')
  server = spawn('go', ['run', '.', 'serve', '--data-dir', dataDir, '--admin-addr', '127.0.0.1:18443', '--observe-addr', '127.0.0.1:19090'], {
    cwd: repository,
    env: { ...process.env, BILI_NOTIFY_LOG_LEVEL: 'warn' },
  })
  await new Promise<void>((resolveReady, reject) => {
    let output = ''
    const timer = setTimeout(() => reject(new Error(`server startup timed out: ${output}`)), 30_000)
    const consume = (chunk: Buffer) => {
      output += chunk.toString()
      const match = output.match(/"setup_code":"([A-Z0-9]+)"/)
      if (match) setupCode = match[1]
      if (setupCode && output.includes('administrator setup required')) {
        clearTimeout(timer)
        setTimeout(resolveReady, 500)
      }
    }
    server.stdout.on('data', consume)
    server.stderr.on('data', consume)
    server.once('exit', code => { clearTimeout(timer); reject(new Error(`server exited with ${code}: ${output}`)) })
  })
})

test.afterAll(() => {
  server?.kill('SIGTERM')
  rmSync(dataDir, { recursive: true, force: true })
})

test('initializes and renders the live operational workspace', async ({ page }) => {
  await page.goto('https://127.0.0.1:18443')
  await page.getByLabel('初始化码').fill(setupCode)
  await page.getByLabel('设置管理员密码').fill('correct horse battery staple')
  await page.getByLabel('确认密码').fill('correct horse battery staple')
  await page.getByRole('button', { name: '初始化并登录' }).click()
  await expect(page.getByRole('heading', { name: '运行概览' })).toBeVisible()
  await page.getByRole('button', { name: 'UP 主', exact: true }).click()
  await page.getByRole('button', { name: '添加 UP 主' }).click()
  await page.getByLabel('UID').fill('42')
  await page.getByLabel('备注名').fill('测试 UP')
  await page.getByRole('button', { name: '保存' }).click()
  await expect(page.getByText('测试 UP')).toBeVisible()
  await page.getByRole('button', { name: '通知渠道', exact: true }).click()
  await page.getByRole('button', { name: '添加渠道' }).click()
  await page.getByLabel('渠道名称').fill('测试企业微信')
  await page.getByLabel('渠道类型').click()
  await page.getByRole('option', { name: '企业微信机器人' }).click()
  await page.getByLabel('Webhook URL').fill('https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=test')
  await page.getByRole('button', { name: '保存' }).click()
  await expect(page.getByText('测试企业微信')).toBeVisible()
  await page.getByRole('button', { name: '概览', exact: true }).click()
  await expect(page.getByRole('heading', { name: '运行概览' })).toBeVisible()
  await expect(page.getByText('服务尚未就绪')).toBeVisible()
  await expect(page).toHaveScreenshot('overview-desktop.png', { fullPage: true })
  await page.setViewportSize({ width: 390, height: 844 })
  await page.emulateMedia({ colorScheme: 'dark' })
  await expect(page.getByRole('heading', { name: '运行概览' })).toBeVisible()
  await expect(page).toHaveScreenshot('overview-mobile.png', { fullPage: true })
})
