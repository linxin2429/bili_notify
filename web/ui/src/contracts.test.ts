import { describe, expect, it } from 'vitest'
import dashboard from '../../testdata/contracts/dashboard.json'
import contentResponses from '../../testdata/contracts/content-responses.json'
import websocketEvents from '../../testdata/contracts/websocket-events.json'
import {
  auditLogPageSchema, commentDetailSchema, commentHistoryPageSchema, dashboardSnapshotSchema,
  dynamicHistoryPageSchema, parseWebsocketEvent, websocketEnvelopeSchema,
} from './contracts'

describe('shared backend contracts', () => {
  it('parses the committed REST dashboard example', () => {
    expect(dashboardSnapshotSchema.parse(dashboard)).toEqual(dashboard)
  })

  it('parses every committed WebSocket event example', () => {
    for (const example of websocketEvents) {
      const envelope = websocketEnvelopeSchema.parse(example)
      expect(parseWebsocketEvent(envelope.event, envelope.data)).toEqual(envelope.data)
    }
  })

  it('parses the committed REST content examples', () => {
    expect(dynamicHistoryPageSchema.parse(contentResponses.dynamics)).toEqual(contentResponses.dynamics)
    expect(commentHistoryPageSchema.parse(contentResponses.comments)).toEqual(contentResponses.comments)
    expect(commentDetailSchema.parse(contentResponses.comment_detail)).toEqual(contentResponses.comment_detail)
    expect(auditLogPageSchema.parse(contentResponses.audit_logs)).toEqual(contentResponses.audit_logs)
  })
})
