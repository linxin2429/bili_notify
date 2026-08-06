import { describe, expect, it } from 'vitest'
import { channelFields } from './channel-form'

describe('channelFields', () => {
  it.each([
    { type: 'email', keys: ['host', 'port', 'tls', 'username', 'password', 'from', 'to'] },
    { type: 'microsoft', keys: ['client_id', 'tenant', 'to'] },
    { type: 'dingtalk', keys: ['webhook', 'secret'] },
    { type: 'feishu', keys: ['webhook', 'secret', 'app_id', 'app_secret'] },
    { type: 'wecom', keys: ['webhook'] },
  ] as const)('defines $type fields', ({ type, keys }) => expect(channelFields(type).map(field => field.key)).toEqual(keys))
})
