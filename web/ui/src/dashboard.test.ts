import { afterEach, describe, expect, it, vi } from 'vitest'
import {
  applyBiliLoginMutation, applyChannelDeletion, applyChannelMutation, applyMicrosoftLoginDeletion,
  applyMicrosoftLoginMutation, applySettingsMutation, applyUpdate, applyUPDeletion, applyUPMutation,
  readinessMessage,
} from './dashboard'
import { makeChannel, makeSnapshot, makeUP, settings } from './test/fixtures'

afterEach(() => vi.useRealTimers())

describe('dashboard mutations', () => {
  it('adds and replaces UPs without mutating the previous snapshot', () => {
    vi.useFakeTimers(); vi.setSystemTime('2026-08-06T12:00:00Z')
    const initial = makeSnapshot({ ups: [] })
    const added = applyUPMutation(initial, makeUP())
    const replaced = applyUPMutation(added, makeUP({ name: '新名称', enabled: false }))
    expect(initial.ups).toEqual([])
    expect(added.ups).toHaveLength(1)
    expect(replaced.ups).toEqual([expect.objectContaining({ name: '新名称', enabled: false })])
    expect(replaced.status).toMatchObject({ up_count: 1, channel_count: 1, ready: false })
  })

  it('adds, replaces, and deletes channels with Microsoft login cleanup', () => {
    const initial = makeSnapshot({ channels: [], microsoft_logins: [{ channel_id: 'channel', status: 'pending' }] })
    const added = applyChannelMutation(initial, makeChannel())
    const replaced = applyChannelMutation(added, makeChannel({ name: '新渠道' }))
    const deleted = applyChannelDeletion(replaced, 'channel')
    expect(replaced.channels).toEqual([expect.objectContaining({ name: '新渠道' })])
    expect(deleted.channels).toEqual([])
    expect(deleted.microsoft_logins).toEqual([])
    expect(deleted.status.channel_count).toBe(0)
  })

  it('applies login and settings mutations while preserving unrelated state', () => {
    const login = { id: 'login', status: 'waiting', expires_at: '2026-08-06T12:05:00Z' }
    const withLogin = applyBiliLoginMutation(makeSnapshot(), login)
    const changed = applySettingsMutation(withLogin, { ...settings, poll_interval_sec: 45 })
    expect(changed.bili_login).toEqual(login)
    expect(changed.settings.poll_interval_sec).toBe(45)
    expect(changed.channels).toHaveLength(1)
    expect(applyBiliLoginMutation(changed, null).bili_login).toBeNull()
  })

  it('adds, replaces, and deletes Microsoft login state', () => {
    const initial = makeSnapshot({ microsoft_logins: [] })
    const added = applyMicrosoftLoginMutation(initial, { channel_id: 'channel', status: 'pending' })
    const replaced = applyMicrosoftLoginMutation(added, { channel_id: 'channel', status: 'success' })
    expect(replaced.microsoft_logins).toEqual([{ channel_id: 'channel', status: 'success' }])
    expect(applyMicrosoftLoginDeletion(replaced, 'channel').microsoft_logins).toEqual([])
  })

  it('deletes only the selected UP', () => {
    const snapshot = makeSnapshot({ ups: [makeUP(), makeUP({ uid: '99' })] })
    expect(applyUPDeletion(snapshot, '42').ups.map(item => item.uid)).toEqual(['99'])
  })
})

describe('dashboard readiness', () => {
  it.each([
    { name: 'missing auth', patch: { auth_valid: false }, ups: [makeUP()], channels: [makeChannel()], want: false },
    { name: 'missing UP', patch: { auth_valid: true }, ups: [], channels: [makeChannel()], want: false },
    { name: 'disabled UP', patch: { auth_valid: true }, ups: [makeUP({ enabled: false })], channels: [makeChannel()], want: false },
    { name: 'missing channel', patch: { auth_valid: true }, ups: [makeUP()], channels: [], want: false },
    { name: 'risk pause', patch: { auth_valid: true, risk_paused_until: '2026-08-07T00:00:00Z' }, ups: [makeUP()], channels: [makeChannel()], want: false },
    { name: 'fresh success', patch: { auth_valid: true, last_success_at: '2026-08-06T11:58:00Z' }, ups: [makeUP()], channels: [makeChannel()], want: true },
    { name: 'stale success', patch: { auth_valid: true, last_success_at: '2026-08-06T11:57:59Z' }, ups: [makeUP()], channels: [makeChannel()], want: false },
    { name: 'invalid success', patch: { auth_valid: true, last_success_at: 'bad' }, ups: [makeUP()], channels: [makeChannel()], want: false },
  ])('$name', ({ patch, ups, channels, want }) => {
    vi.useFakeTimers(); vi.setSystemTime('2026-08-06T12:00:00Z')
    const snapshot = makeSnapshot({ status: { ...makeSnapshot().status, ...patch }, settings: { ...settings, poll_interval_sec: 60 }, ups, channels })
    const next = applyUPMutation(snapshot, ups[0] || makeUP({ enabled: false }))
    expect(next.status.ready).toBe(want)
  })

  it.each([
    { name: 'auth', snapshot: makeSnapshot({ status: { ...makeSnapshot().status, auth_valid: false } }), want: '扫码登录' },
    { name: 'channel', snapshot: makeSnapshot({ channels: [] }), want: '通知渠道' },
    { name: 'UP', snapshot: makeSnapshot({ ups: [] }), want: 'UP 主' },
    { name: 'risk', snapshot: makeSnapshot({ status: { ...makeSnapshot().status, risk_paused_until: 'later' } }), want: '风控' },
    { name: 'first collection', snapshot: makeSnapshot({ status: { ...makeSnapshot().status, last_success_at: undefined } }), want: '首次采集' },
    { name: 'ready', snapshot: makeSnapshot(), want: '均正常' },
  ])('reports the first $name blocker', ({ snapshot, want }) => expect(readinessMessage(snapshot)).toContain(want))
})

describe('realtime reducer', () => {
  it.each([
    ['status.updated', 'status'], ['settings.updated', 'settings'], ['ups.updated', 'ups'],
    ['channels.updated', 'channels'], ['deliveries.updated', 'deliveries'], ['bilibili.login.updated', 'bili_login'],
    ['microsoft.login.updated', 'microsoft_logins'],
  ] as const)('applies %s', (event, field) => {
    const snapshot = makeSnapshot()
    const value = field === 'status' ? { ...snapshot.status, ready: false } : field === 'settings' ? settings : field === 'ups' ? [] : field === 'channels' ? [] : field === 'deliveries' ? [] : field === 'bili_login' ? null : []
    expect(applyUpdate(snapshot, event, value)?.[field]).toEqual(value)
  })

  it('keeps null and ignores unknown events', () => {
    expect(applyUpdate(null, 'ups.updated', [])).toBeNull()
    const snapshot = makeSnapshot()
    expect(applyUpdate(snapshot, 'future.event', {})?.ups).toEqual(snapshot.ups)
  })
})
