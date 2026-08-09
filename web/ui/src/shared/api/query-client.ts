import { QueryClient } from '@tanstack/react-query'

export function createQueryClient() {
  return new QueryClient({ defaultOptions: { queries: { refetchOnWindowFocus: true, retry: (count, error) => count < 2 && !(error instanceof Error && error.name === 'ApiError' && 'retryable' in error && !(error as { retryable: boolean }).retryable) }, mutations: { retry: false } } })
}
