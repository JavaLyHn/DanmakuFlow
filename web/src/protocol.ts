import { isLosslessNumber, parse } from 'lossless-json'

export interface ConnectionConfig { port: string; room: string; uid: string }
const maxUint64 = 18446744073709551615n
function uint64(value: string): boolean { return /^\d+$/.test(value) && BigInt(value) <= maxUint64 }

export function generateUid(): string {
  // Timestamp-shaped identifier, not a claim of nanosecond clock resolution.
  const random = crypto.getRandomValues(new Uint32Array(1))[0]!
  return (BigInt(Date.now()) * 1000000n + BigInt(random % 1000000)).toString()
}

export function connectionUrl(config: ConnectionConfig): string {
  if (!/^\d+$/.test(config.port) || Number(config.port) < 1 || Number(config.port) > 65535) throw Error('服务端端口必须在 1–65535 之间。')
  if (!uint64(config.uid)) throw Error('用户 ID 必须是 uint64 范围内的十进制整数。')
  if (config.room === '') throw Error('请填写房间 ID。')
  const url = new URL(`ws://127.0.0.1:${config.port}/ws`)
  url.search = new URLSearchParams({ uid: config.uid, name: config.uid, room: config.room }).toString()
  return url.toString()
}

export function encodeInput(text: string): string {
  return JSON.stringify(text === '1' ? { type: 103, data: null } : { type: 101, data: { content: text } })
}

type Incoming = { kind: 'message'; content: string } | { kind: 'stats'; online: string; likes: string }
function object(value: unknown): value is Record<string, unknown> { return typeof value === 'object' && value !== null && !Array.isArray(value) }
function count(value: unknown): string | null {
  if (!isLosslessNumber(value) || !uint64(value.value)) return null
  return BigInt(value.value).toString()
}

export function decodePacket(raw: string): Incoming | null {
  try {
    const packet = parse(raw)
    if (!object(packet) || !isLosslessNumber(packet.type) || !object(packet.data)) return null
    if (packet.type.value === '101' && typeof packet.data.content === 'string') return { kind: 'message', content: packet.data.content }
    if (packet.type.value === '102') {
      const online = count(packet.data.online), likes = count(packet.data.likes)
      if (online !== null && likes !== null) return { kind: 'stats', online, likes }
    }
  } catch { /* Match the terminal's silent handling of invalid envelopes. */ }
  return null
}
