import type { ChannelType } from '../types'

export interface ChannelField { key: string; label: string; required?: boolean; secret?: boolean; help?: string; defaultValue?: string }

export function channelFields(type: ChannelType): ChannelField[] {
  const fields: Record<ChannelType, ChannelField[]> = {
    email: [
      { key: 'host', label: 'SMTP 主机', required: true }, { key: 'port', label: '端口', required: true, defaultValue: '465' },
      { key: 'tls', label: 'TLS 模式（tls 或 starttls）', required: true, defaultValue: 'tls' }, { key: 'username', label: '用户名' },
      { key: 'password', label: '密码', secret: true }, { key: 'from', label: '发件人', required: true }, { key: 'to', label: '收件人', required: true, help: '多个地址使用英文逗号分隔' },
    ],
    microsoft: [{ key: 'client_id', label: '应用程序（客户端）ID', required: true }, { key: 'tenant', label: '租户', defaultValue: 'common' }, { key: 'to', label: '收件人', required: true, help: '多个地址使用英文逗号分隔' }],
    dingtalk: [{ key: 'webhook', label: 'Webhook URL', required: true, secret: true }, { key: 'secret', label: '签名密钥', required: true, secret: true, help: '钉钉自定义机器人仅支持公开图链，图片使用 B 站 CDN 外链。' }],
    feishu: [{ key: 'webhook', label: 'Webhook URL', required: true, secret: true }, { key: 'secret', label: '签名密钥', required: true, secret: true }, { key: 'app_id', label: '应用 App ID', help: '可选。配置后图片以上传 image_key 发送；不配置则图片显示为链接。' }, { key: 'app_secret', label: '应用 App Secret', secret: true, help: '与 App ID 成对配置。' }],
    wecom: [{ key: 'webhook', label: 'Webhook URL', required: true, secret: true }],
  }
  return fields[type]
}
