import { useQuery } from '@tanstack/react-query'
import { ChevronLeft, ChevronRight, Search, SearchX, SlidersHorizontal, X } from 'lucide-react'
import { useCallback, useEffect, useState } from 'react'
import { useSearchParams } from 'react-router-dom'
import type { ContentQuery } from '../shared/api/types'
import { queries } from '../shared/api/query'
import { Button, EmptyState, LoadingState, NativeDateTimeField, PageError, PageHeader, SelectField, TextField } from '../shared/ui'
import { localInputToRFC3339 } from '../shared/lib/presentation'
import { advanceCursor, rewindCursor } from '../shared/lib/cursor-params'
import { HistoryCard } from '../modules/history'

export function HistoryPage() {
  const [params, setParams] = useSearchParams()
  const platform = params.get('platform') === 'bilibili' || params.get('platform') === 'zsxq' ? params.get('platform') as 'bilibili' | 'zsxq' : undefined
  const sourceID = params.get('source_id') || ''
  const q = params.get('q') || ''
  const from = params.get('from') || ''
  const to = params.get('to') || ''
  const after = params.get('after') || ''
  const stack = params.get('stack') || ''
  const advancedFilterCount = [platform, sourceID, from, to].filter(Boolean).length
  const activeFilterCount = advancedFilterCount + (q ? 1 : 0)
  const [draftQ, setDraftQ] = useState(q)
  const [filtersOpen, setFiltersOpen] = useState(advancedFilterCount > 0)
  const runtime = useQuery(queries.runtime())
  const sources = useQuery(queries.sources(platform || ''))
  const query: ContentQuery = {
    ...(platform && { platform }), ...(sourceID && { source_id: sourceID }), ...(q && { q }),
    ...(from && { from: localInputToRFC3339(from) }), ...(to && { to: localInputToRFC3339(to) }), ...(after && { after }), limit: 20,
  }
  const contents = useQuery(queries.contents(query))
  const update = useCallback((patch: Record<string, string | undefined>, resetCursor = true) => {
    const next = new URLSearchParams(params)
    for (const [key, value] of Object.entries(patch)) {
      if (value) next.set(key, value)
      else next.delete(key)
    }
    if (resetCursor && !('after' in patch) && !('stack' in patch)) {
      next.delete('after')
      next.delete('stack')
    }
    setParams(next)
  }, [params, setParams])
  useEffect(() => {
    const timer = window.setTimeout(() => { if (draftQ !== q) update({ q: draftQ || undefined }) }, 300)
    return () => window.clearTimeout(timer)
  }, [draftQ, q, update])
  const clearFilters = () => {
    setDraftQ('')
    update({ platform: undefined, source_id: undefined, q: undefined, from: undefined, to: undefined })
  }

  if (runtime.isPending || sources.isPending) return <LoadingState />
  if (runtime.error || sources.error) return <PageError error={runtime.error || sources.error} retry={() => { void runtime.refetch(); void sources.refetch() }} />
  return <div className="page-stack">
    <PageHeader title="历史内容" subtitle="统一浏览 B 站与知识星球归档；档案更新不会重复通知。" />
    <section className="history-filters" aria-label="历史内容筛选">
      <div className="history-search-row">
        <div className="history-search"><Search aria-hidden="true" /><TextField label="关键字" value={draftQ} onChange={setDraftQ} /></div>
        <Button variant="outline" className="filter-toggle" onPress={() => setFiltersOpen(value => !value)} aria-expanded={filtersOpen}>
          <SlidersHorizontal aria-hidden="true" />更多筛选{advancedFilterCount > 0 && <span className="filter-count">{advancedFilterCount}</span>}
        </Button>
        {activeFilterCount > 0 && <Button onPress={clearFilters}><X aria-hidden="true" />清除筛选</Button>}
      </div>
      {filtersOpen && <div className="filter-grid filter-grid--advanced">
        <SelectField label="平台" value={platform || ''} onChange={value => update({ platform: value || undefined, source_id: undefined })} options={[{ value: '', label: '全部平台' }, { value: 'bilibili', label: 'B 站' }, { value: 'zsxq', label: '知识星球' }]} />
        <SelectField label="采集源" value={sourceID} onChange={value => update({ source_id: value || undefined })} options={[{ value: '', label: '全部来源' }, ...sources.data.map(source => ({ value: source.id, label: source.name || source.external_id }))]} />
        <NativeDateTimeField label="开始时间" value={from} onChange={value => update({ from: value || undefined })} />
        <NativeDateTimeField label="结束时间" value={to} onChange={value => update({ to: value || undefined })} />
      </div>}
    </section>
    {contents.isPending ? <LoadingState /> : contents.error ? <PageError error={contents.error} retry={() => void contents.refetch()} /> : contents.data.items.length === 0 ? <EmptyState icon={<SearchX />} title="当前筛选下没有历史记录" /> : <div className="list-stack">{contents.data.items.map(item => (
      <HistoryCard
        key={item.id}
        item={item}
        timeZone={runtime.data.timezone}
        sourceName={sources.data.find(source => source.id === item.source_id)?.name}
      />
    ))}</div>}
    {contents.data && <div className="pagination">
      <Button isDisabled={!after && !stack} onPress={() => update(rewindCursor(stack), false)}><ChevronLeft aria-hidden="true" />上一页</Button>
      <Button isDisabled={!contents.data.page.has_more} onPress={() => update(advanceCursor(after, stack, contents.data.page.next_cursor), false)}>下一页<ChevronRight aria-hidden="true" /></Button>
    </div>}
  </div>
}
