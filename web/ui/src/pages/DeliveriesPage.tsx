import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useSearchParams } from 'react-router-dom'
import { queries } from '../shared/api/query'
import { resources } from '../shared/api/resources'
import { apiErrorMessage } from '../shared/api/errors'
import { useSession } from '../modules/session/session'
import { Badge, Button, Card, EmptyState, LoadingState, PageError, PageHeader, useNotify } from '../shared/ui'
import { deliverySummary, deliveryTitle, formatDate } from '../presentation'

export function DeliveriesPage() {
  const [params, setParams] = useSearchParams(); const after = params.get('after') || ''; const requested = params.get('state'); const state = requested === 'pending' || requested === 'blocked' ? requested : 'all'
  const deliveries = useQuery(queries.deliveries(after)); const channels = useQuery(queries.channels()); const runtime = useQuery(queries.runtime()); const { csrf } = useSession(); const client = useQueryClient(); const notify = useNotify()
  const retry = useMutation({ mutationFn: (id: string) => resources.retryDelivery(csrf, id), onSuccess: () => { notify('已重新加入投递队列', 'success'); void client.invalidateQueries({ queryKey: ['deliveries'] }) }, onError: error => notify(apiErrorMessage(error), 'danger') })
  const update = (patch: Record<string, string | undefined>) => { const next = new URLSearchParams(params); for (const [key, value] of Object.entries(patch)) { if (value) next.set(key, value); else next.delete(key) } setParams(next) }
  if (deliveries.isPending || channels.isPending || runtime.isPending) return <LoadingState />
  if (deliveries.error || channels.error || runtime.error) return <PageError error={deliveries.error || channels.error || runtime.error} retry={() => { void deliveries.refetch(); void channels.refetch(); void runtime.refetch() }} />
  const items = deliveries.data.items.filter(item => state === 'all' || item.state === state)
  return <div className="page-stack"><PageHeader title="投递队列" subtitle="按稳定游标读取待投递任务；新任务不会扰动当前页。" />
    <div className="tabs" role="tablist" aria-label="投递状态">{[{ key: 'all', label: '全部' }, { key: 'pending', label: '等待重试' }, { key: 'blocked', label: '已阻塞' }].map(item => <Button role="tab" aria-selected={state === item.key} key={item.key} className={state === item.key ? 'tab--active' : ''} onPress={() => update({ state: item.key === 'all' ? undefined : item.key, after: undefined })}>{item.label}</Button>)}</div>
    {items.length === 0 ? <EmptyState icon="✓" title="当前筛选下没有待投递任务" /> : <div className="list-stack">{items.map(delivery => <Card key={delivery.id}><div className="delivery"><div><div className="card-title-inline"><Badge tone={delivery.state === 'blocked' ? 'danger' : 'warning'}>{delivery.state === 'blocked' ? '已阻塞' : '等待重试'}</Badge><strong>{deliveryTitle(delivery)}</strong></div><p className="summary-clamp">{deliverySummary(delivery)}</p><p className="muted">渠道：{channels.data.find(channel => channel.id === delivery.channel_id)?.name || delivery.channel_id} · 已尝试 {delivery.attempts} 次</p>{delivery.last_error && <p className="danger-text">上次错误：{delivery.last_error}</p>}</div><div className="delivery__action"><small>下次处理</small><span>{formatDate(delivery.next_at, runtime.data.timezone)}</span>{delivery.state === 'blocked' && <Button variant="outline" busy={retry.isPending && retry.variables === delivery.id} onPress={() => retry.mutate(delivery.id)}>↻ 立即重试</Button>}</div></div></Card>)}</div>}
    <div className="pagination"><Button isDisabled={!after} onPress={() => history.back()}>← 上一页</Button><Button isDisabled={!deliveries.data.page.has_more} onPress={() => update({ after: deliveries.data.page.next_cursor })}>下一页 →</Button></div>
  </div>
}
