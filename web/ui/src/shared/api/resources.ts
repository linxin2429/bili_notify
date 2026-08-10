import { z } from 'zod'
import {
  auditLogPageSchema, biliLoginSchema, channelSchema, commentDetailSchema, commentHistoryPageSchema,
  deliveryPageSchema, dynamicDetailSchema, dynamicHistoryPageSchema, emptyResponseSchema, microsoftLoginSchema, queuedStatusSchema,
  runtimeSchema, runtimeSettingsSchema, sentStatusSchema, upSchema,
  aiWorkerStatusSchema, aiProfileSchema, aiProfileTestResultSchema, aiPromptSchema, aiJobSchema, aiJobPageSchema, canceledStatusSchema,
} from './contracts'
import type { AIProfileDraft, AIPromptDraft, AuditQuery, ChannelDraft, ContentQuery, RuntimeSettings, UP } from './types'
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
  dynamic: (id: string, signal?: AbortSignal) => requestJSON(`${apiRoot}/dynamics/${encodeURIComponent(id)}`, dynamicDetailSchema, { signal }),
  comments: (query: ContentQuery, signal?: AbortSignal) => requestJSON(`${apiRoot}/comments${queryString(query)}`, commentHistoryPageSchema, { signal }),
  comment: (rpid: string, signal?: AbortSignal) => requestJSON(`${apiRoot}/comments/${encodeURIComponent(rpid)}`, commentDetailSchema, { signal }),
  auditLogs: (query: AuditQuery, signal?: AbortSignal) => requestJSON(`${apiRoot}/audit-logs${queryString(query)}`, auditLogPageSchema, { signal }),
  aiStatus: (signal?: AbortSignal) => requestJSON(`${apiRoot}/ai/status`, aiWorkerStatusSchema, { signal }),
  aiProfiles: (signal?: AbortSignal) => requestJSON(`${apiRoot}/ai/profiles`, array(aiProfileSchema), { signal }),
  aiPrompts: (signal?: AbortSignal) => requestJSON(`${apiRoot}/ai/prompts`, array(aiPromptSchema), { signal }),
  aiJobs: (query: { kind?: string; state?: string; limit?: number; offset?: number } = {}, signal?: AbortSignal) => requestJSON(`${apiRoot}/ai/jobs${queryString(query)}`, aiJobPageSchema, { signal }),
  aiJob: (id: string, signal?: AbortSignal) => requestJSON(`${apiRoot}/ai/jobs/${encodeURIComponent(id)}`, aiJobSchema, { signal }),

  createUP: (csrf: string, input: Pick<UP, 'uid' | 'name' | 'enabled'>) => write(`${apiRoot}/ups`, upSchema, 'POST', csrf, input),
  updateUP: (csrf: string, input: Pick<UP, 'uid' | 'name' | 'enabled'>) => write(`${apiRoot}/ups/${encodeURIComponent(input.uid)}`, upSchema, 'PUT', csrf, { name: input.name, enabled: input.enabled }),
  deleteUP: (csrf: string, uid: string) => write(`${apiRoot}/ups/${encodeURIComponent(uid)}`, emptyResponseSchema, 'DELETE', csrf),
  createChannel: (csrf: string, input: ChannelDraft) => write(`${apiRoot}/channels`, channelSchema, 'POST', csrf, input),
  updateChannel: (csrf: string, input: ChannelDraft & { id: string }) => {
    const { id, ...body } = input
    return write(`${apiRoot}/channels/${encodeURIComponent(id)}`, channelSchema, 'PUT', csrf, body)
  },
  deleteChannel: (csrf: string, id: string) => write(`${apiRoot}/channels/${encodeURIComponent(id)}`, emptyResponseSchema, 'DELETE', csrf),
  testChannel: (csrf: string, id: string) => write(`${apiRoot}/channels/${encodeURIComponent(id)}/test`, sentStatusSchema, 'POST', csrf),
  retryDelivery: (csrf: string, id: string) => write(`${apiRoot}/deliveries/${encodeURIComponent(id)}/retry`, queuedStatusSchema, 'POST', csrf),
  startBiliLogin: (csrf: string) => write(`${apiRoot}/bilibili-login`, biliLoginSchema.unwrap(), 'POST', csrf),
  cancelBiliLogin: (csrf: string, id: string) => write(`${apiRoot}/bilibili-login/${encodeURIComponent(id)}`, emptyResponseSchema, 'DELETE', csrf),
  startMicrosoftLogin: (csrf: string, channelID: string) => write(`${apiRoot}/channels/${encodeURIComponent(channelID)}/microsoft-login`, microsoftLoginSchema, 'POST', csrf),
  cancelMicrosoftLogin: (csrf: string, channelID: string) => write(`${apiRoot}/channels/${encodeURIComponent(channelID)}/microsoft-login`, emptyResponseSchema, 'DELETE', csrf),
  updateSettings: (csrf: string, settings: RuntimeSettings) => write(`${apiRoot}/settings`, runtimeSettingsSchema, 'PUT', csrf, settings),
  createAIProfile: (csrf: string, input: AIProfileDraft) => write(`${apiRoot}/ai/profiles`, aiProfileSchema, 'POST', csrf, aiProfileBody(input)),
  updateAIProfile: (csrf: string, input: AIProfileDraft & { id: string }) => write(`${apiRoot}/ai/profiles/${encodeURIComponent(input.id)}`, aiProfileSchema, 'PUT', csrf, aiProfileBody(input)),
  updateAIProfileAvailability: (csrf: string, id: string, enabled: boolean) => write(`${apiRoot}/ai/profiles/${encodeURIComponent(id)}/availability`, aiProfileSchema, 'PUT', csrf, { enabled }),
  deleteAIProfile: (csrf: string, id: string) => write(`${apiRoot}/ai/profiles/${encodeURIComponent(id)}`, emptyResponseSchema, 'DELETE', csrf),
  testAIProfile: (csrf: string, id: string) => write(`${apiRoot}/ai/profiles/${encodeURIComponent(id)}/test`, aiProfileTestResultSchema, 'POST', csrf),
  createAIPrompt: (csrf: string, input: AIPromptDraft) => write(`${apiRoot}/ai/prompts`, aiPromptSchema, 'POST', csrf, input),
  updateAIPrompt: (csrf: string, input: AIPromptDraft & { id: string }) => { const { id, ...body } = input; return write(`${apiRoot}/ai/prompts/${encodeURIComponent(id)}`, aiPromptSchema, 'PUT', csrf, body) },
  deleteAIPrompt: (csrf: string, id: string) => write(`${apiRoot}/ai/prompts/${encodeURIComponent(id)}`, emptyResponseSchema, 'DELETE', csrf),
  createAITranscription: (csrf: string, input: { client_request_id: string; bvid: string; page?: number; profile_id: string }) => write(`${apiRoot}/ai/transcriptions`, aiJobSchema, 'POST', csrf, input),
  createAISummary: (csrf: string, input: { client_request_id: string; text?: string; transcription_job_id?: string; profile_id: string; prompt_id: string }) => write(`${apiRoot}/ai/summaries`, aiJobSchema, 'POST', csrf, input),
  cancelAIJob: (csrf: string, id: string) => write(`${apiRoot}/ai/jobs/${encodeURIComponent(id)}/cancel`, canceledStatusSchema, 'POST', csrf),
  retryAIJob: (csrf: string, id: string) => write(`${apiRoot}/ai/jobs/${encodeURIComponent(id)}/retry`, queuedStatusSchema, 'POST', csrf),
  deleteAIJob: (csrf: string, id: string) => write(`${apiRoot}/ai/jobs/${encodeURIComponent(id)}`, emptyResponseSchema, 'DELETE', csrf),
}

function write<T>(path: string, schema: z.ZodType<T>, method: string, csrf: string, body?: unknown) {
  return requestJSON(path, schema, { method, csrf, ...(body === undefined ? {} : { body: JSON.stringify(body) }) })
}

function aiProfileBody(input: AIProfileDraft): AIProfileDraft {
  return {
    name: input.name,
    kind: input.kind,
    base_url: input.base_url,
    model: input.model,
    api_key: input.api_key,
    language: input.language,
    prompt: input.prompt,
    temperature: input.temperature,
    max_output_tokens: input.max_output_tokens,
    context_window_chars: input.context_window_chars,
    timeout_sec: input.timeout_sec,
    enabled: input.enabled,
    default: input.default,
  }
}
