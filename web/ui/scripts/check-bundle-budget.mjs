import { readFileSync } from 'node:fs'
import { gzipSync } from 'node:zlib'
import { resolve } from 'node:path'

const kibibyte = 1024
const budgets = {
  entry: 120 * kibibyte,
  route: 40 * kibibyte,
  historyRoute: 60 * kibibyte,
}

const uiDirectory = resolve(import.meta.dirname, '..')
const outputDirectory = resolve(uiDirectory, '../dist')
const manifestPath = resolve(outputDirectory, '.vite/manifest.json')

let manifest
try {
  manifest = JSON.parse(readFileSync(manifestPath, 'utf8'))
} catch (error) {
  console.error(`无法读取 Vite manifest：${manifestPath}`)
  console.error('请先运行 npm run build。')
  console.error(error instanceof Error ? error.message : String(error))
  process.exit(1)
}

const entries = Object.values(manifest)

function javascriptFilesFor(entry, seen = new Set()) {
  if (!entry || seen.has(entry.file)) return seen
  if (entry.file.endsWith('.js')) seen.add(entry.file)

  for (const importedKey of entry.imports ?? []) {
    javascriptFilesFor(manifest[importedKey], seen)
  }
  return seen
}

function gzipBytes(files) {
  return [...files].reduce((total, file) => {
    const content = readFileSync(resolve(outputDirectory, file))
    return total + gzipSync(content, { level: 9 }).byteLength
  }, 0)
}

function formatKib(bytes) {
  return `${(bytes / kibibyte).toFixed(2)} KiB gzip`
}

function sourceOf(entry) {
  return entry.src ?? entry.name ?? entry.file
}

const applicationEntry = entries.find(entry => entry.isEntry && sourceOf(entry).endsWith('src/main.tsx'))
  ?? entries.find(entry => entry.isEntry)

if (!applicationEntry) {
  console.error('Vite manifest 中没有应用入口，无法执行 bundle 预算检查。')
  process.exit(1)
}

const initialFiles = javascriptFilesFor(applicationEntry)
const checks = [{
  label: 'initial',
  bytes: gzipBytes(initialFiles),
  budget: budgets.entry,
  files: initialFiles,
}]

const routeEntries = entries.filter(entry => {
  const source = sourceOf(entry)
  return entry.isDynamicEntry === true && /(?:^|\/)src\/pages\//.test(source)
})

if (routeEntries.length === 0) {
  console.error('Vite manifest 中没有 src/pages 下的动态路由入口。所有业务页面必须使用路由级懒加载。')
  process.exit(1)
}

for (const routeEntry of routeEntries) {
  const routeFiles = javascriptFilesFor(routeEntry)
  for (const initialFile of initialFiles) routeFiles.delete(initialFile)

  const source = sourceOf(routeEntry)
  const isHistory = /(?:^|\/)(?:history(?:\/|$)|HistoryPage\.)/i.test(source)
  checks.push({
    label: source,
    bytes: gzipBytes(routeFiles),
    budget: isHistory ? budgets.historyRoute : budgets.route,
    files: routeFiles,
  })
}

let failed = false
for (const check of checks) {
  const passed = check.bytes <= check.budget
  const marker = passed ? 'PASS' : 'FAIL'
  console.log(`${marker} ${check.label}: ${formatKib(check.bytes)} / ${formatKib(check.budget)}`)
  console.log(`     ${[...check.files].sort().join(', ')}`)
  failed ||= !passed
}

if (failed) {
  console.error('Bundle 超出预算。请根据 manifest 中列出的实际依赖拆包或减重，不得提高预算以掩盖回退。')
  process.exit(1)
}
