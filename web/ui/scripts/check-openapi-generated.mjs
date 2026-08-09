import { existsSync, readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { spawnSync } from 'node:child_process'

const uiDirectory = resolve(import.meta.dirname, '..')
const repositoryDirectory = resolve(uiDirectory, '../..')
const packageJSON = JSON.parse(readFileSync(resolve(uiDirectory, 'package.json'), 'utf8'))
const generateScript = packageJSON.scripts?.['api:generate']

if (!generateScript) {
  console.error('缺少 npm script `api:generate`。OpenAPI 生成器落地后才能启用契约门禁。')
  process.exit(1)
}

const generatedPaths = (process.env.OPENAPI_GENERATED_PATHS
  ?? 'api/openapi.yaml web/generated web/ui/src/shared/api/generated')
  .split(/\s+/)
  .filter(Boolean)

if (!existsSync(resolve(repositoryDirectory, 'api/openapi.yaml'))) {
  console.error('缺少 api/openapi.yaml，无法验证生成契约。')
  process.exit(1)
}

const generation = spawnSync('npm', ['run', 'api:generate'], {
  cwd: uiDirectory,
  encoding: 'utf8',
  stdio: 'inherit',
})

if (generation.error) {
  console.error(`无法运行 OpenAPI 生成器：${generation.error.message}`)
  process.exit(1)
}
if (generation.status !== 0) process.exit(generation.status ?? 1)

const status = spawnSync(
  'git',
  ['status', '--porcelain', '--untracked-files=all', '--', ...generatedPaths],
  { cwd: repositoryDirectory, encoding: 'utf8' },
)

if (status.error) {
  console.error(`无法检查生成结果：${status.error.message}`)
  process.exit(1)
}
if (status.status !== 0) {
  process.stderr.write(status.stderr)
  process.exit(status.status ?? 1)
}

if (status.stdout.trim() !== '') {
  console.error('OpenAPI 生成结果与仓库不一致，请重新生成并提交以下文件：')
  console.error(status.stdout.trimEnd())
  process.exit(1)
}

console.log(`OpenAPI 生成结果已同步：${generatedPaths.join(', ')}`)
