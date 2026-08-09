const environment = (globalThis as { process?: { env?: Record<string, string | undefined> } }).process?.env

export function configuredWorkerCount(value = environment?.CI_TOOL_WORKERS): number | undefined {
  if (value === undefined || value.trim() === '') return undefined
  if (!/^[1-9]\d*$/.test(value)) throw new Error('CI_TOOL_WORKERS must be a positive integer')
  const workers = Number(value)
  if (!Number.isSafeInteger(workers)) throw new Error('CI_TOOL_WORKERS must be a positive safe integer')
  return workers
}
