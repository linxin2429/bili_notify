import { z } from 'zod'
import {
  auditLogPageSchema, biliLoginSchema, channelSchema, commentDetailSchema, commentHistoryPageSchema,
  deliveryPageSchema, dynamicHistoryPageSchema, emptyResponseSchema, microsoftLoginSchema, queuedStatusSchema,
  runtimeSchema, runtimeSettingsSchema, sentStatusSchema, upSchema,
} from '../../contracts'
import type { AuditQuery, ChannelDraft, ContentQuery, RuntimeSettings, UP } from '../../types'
import { queryString, requestJSON } from './client'

const apiRoot = '/api/v2'
const array = <T extends z.ZodType>(schema: T) => z.array(schema)

export const resources = {
  runtime: (signal?: AbortSignal) => requestJSON(`${apiRoot}/runtime`, runtimeSchema, { signal }),
  settings: (signal?: AbortSignal) => requestJSON(`${apiRoot}/settings`, runtimeSettingsSchema, { signal }),
  ups: (signal?: AbortSignal) => requestJSON(`${apiRoot}/ups`, array(upSchema), { signal }),
  channels: (signal?: AbortSignal) => requestJSON(`${apiRoot}/channels`, array(channelSchema), { signal }),
  deliveries: (after = '', signal?: AbortSignal) => requestJSON(`${apiRoot}/deliveries${queryString({ limit: 20, after })}`, deliveryPageSchema, { signal }),
  biliLogin: (signal?: AbortSignal) => requestJSON(`${apiRoot}/bilibili-login`, biliLoginSchema, { signal }),
  microsoftLogins: (signal?: AbortSignal) => requestJSON(`${apiRoot}/microsoft-logins`, array(microsoftLoginSchema), { signal }),
  dynamics: (query: ContentQuery, signal?: AbortSignal) => requestJSON(`${apiRoot}/dynamics${queryString(query)}`, dynamicHistoryPageSchema, { signal }),
  comments: (query: ContentQuery, signal?: AbortSignal) => requestJSON(`${apiRoot}/comments${queryString(query)}`, commentHistoryPageSchema, { signal }),
  comment: (rpid: string, signal?: AbortSignal) => requestJSON(`${apiRoot}/comments/${encodeURIComponent(rpid)}`, commentDetailSchema, { signal }),
  auditLogs: (query: AuditQuery, signal?: AbortSignal) => requestJSON(`${apiRoot}/audit-logs${queryString(query)}`, auditLogPageSchema, { signal }),

  createUP: (csrf: string, input: Pick<UP, 'uid' | 'name' | 'enabled'>) => write(`${apiRoot}/ups`, upSchema, 'POST', csrf, input),
  updateUP: (csrf: string, input: Pick<UP, 'uid' | 'name' | 'enabled'>) => write(`${apiRoot}/ups/${encodeURIComponent(input.uid)}`, upSchema, 'PUT', csrf, { name: input.name, enabled: input.enabled }),
  deleteUP: (csrf: string, uid: string) => write(`${apiRoot}/ups/${encodeURIComponent(uid)}`, emptyResponseSchema, 'DELETE', csrf),
  createChannel: (csrf: string, input: ChannelDraft) => write(`${apiRoot}/channels`, channelSchema, 'POST', csrf, input),
  updateChannel: (csrf: string, input: ChannelDraft & { id: string }) => write(`${apiRoot}/channels/${encodeURIComponent(input.id)}`, channelSchema, 'PUT', csrf, input),
  deleteChannel: (csrf: string, id: string) => write(`${apiRoot}/channels/${encodeURIComponent(id)}`, emptyResponseSchema, 'DELETE', csrf),
  testChannel: (csrf: string, id: string) => write(`${apiRoot}/channels/${encodeURIComponent(id)}/test`, sentStatusSchema, 'POST', csrf),
  retryDelivery: (csrf: string, id: string) => write(`${apiRoot}/deliveries/${encodeURIComponent(id)}/retry`, queuedStatusSchema, 'POST', csrf),
  startBiliLogin: (csrf: string) => write(`${apiRoot}/bilibili-login`, biliLoginSchema.unwrap(), 'POST', csrf),
  cancelBiliLogin: (csrf: string, id: string) => write(`${apiRoot}/bilibili-login/${encodeURIComponent(id)}`, emptyResponseSchema, 'DELETE', csrf),
  startMicrosoftLogin: (csrf: string, channelID: string) => write(`${apiRoot}/channels/${encodeURIComponent(channelID)}/microsoft-login`, microsoftLoginSchema, 'POST', csrf),
  cancelMicrosoftLogin: (csrf: string, channelID: string) => write(`${apiRoot}/channels/${encodeURIComponent(channelID)}/microsoft-login`, emptyResponseSchema, 'DELETE', csrf),
  updateSettings: (csrf: string, settings: RuntimeSettings) => write(`${apiRoot}/settings`, runtimeSettingsSchema, 'PUT', csrf, settings),
}

function write<T>(path: string, schema: z.ZodType<T>, method: string, csrf: string, body?: unknown) {
  return requestJSON(path, schema, { method, csrf, ...(body === undefined ? {} : { body: JSON.stringify(body) }) })
}
