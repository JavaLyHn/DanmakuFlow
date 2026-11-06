import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { useRoomSession } from './useRoomSession'

class FakeSocket {
  static instances: FakeSocket[] = []
  readyState = 0
  onopen: (() => void) | null = null
  onmessage: ((e: { data: string }) => void) | null = null
  onerror: (() => void) | null = null
  onclose: ((e: { wasClean: boolean; code: number; reason: string }) => void) | null = null
  sent: string[] = []
  failSend = false
  failClose = false
  constructor(public url: string) { FakeSocket.instances.push(this) }
  send(data: string) { if (this.failSend) throw Error('write failure'); this.sent.push(data) }
  close() { if (this.failClose) throw Error('close failure'); this.readyState = 3 }
  open() { this.readyState = 1; this.onopen?.() }
  message(data: string) { this.onmessage?.({ data }) }
  ended(clean = false) { this.readyState = 3; this.onclose?.({ wasClean: clean, code: clean ? 1000 : 1006, reason: '' }) }
}
beforeEach(() => { vi.useFakeTimers(); FakeSocket.instances = []; vi.stubGlobal('WebSocket', FakeSocket) })
afterEach(() => { vi.useRealTimers(); vi.unstubAllGlobals() })
function start() { const room = useRoomSession(); room.connect(); const socket = FakeSocket.instances[0]!; socket.open(); return { room, socket } }

describe('room session', () => {
  it('starts empty, locks one connection and generates a precise UID', () => {
    const room = useRoomSession()
    expect(room.online.value).toBeNull(); expect(room.messages).toHaveLength(0)
    room.connect(); room.connect()
    expect(FakeSocket.instances).toHaveLength(1)
    expect(room.status.value).toBe('connecting'); expect(room.locked.value).toBe(true)
    expect(room.config.uid).toMatch(/^\d+$/)
    room.dispose()
  })
  it('sends without optimistic insertion or counter changes', () => {
    const { room, socket } = start()
    expect(room.send('')).toBe(true); expect(room.send('1')).toBe(true); expect(room.like()).toBe(true)
    expect(socket.sent.map(x => JSON.parse(x))).toEqual([{ type: 101, data: { content: '' } }, { type: 103, data: null }, { type: 103, data: null }])
    expect(room.messages).toHaveLength(0); expect(room.likes.value).toBeNull()
    socket.failSend = true; expect(room.send('draft')).toBe(false); expect(room.feedbackError.value).toBe(true)
    room.dispose()
  })
  it('receives independent stats and ordered batched messages, preserving data on disconnect', () => {
    const { room, socket } = start()
    socket.message('{"type":102,"data":{"online":18,"likes":126}}')
    expect(room.online.value).toBe('18'); expect(room.messages).toHaveLength(0)
    socket.message('{"type":101,"data":{"content":"first"}}')
    socket.message('{"type":101,"data":{"content":"second"}}')
    room.disconnect()
    expect(room.messages.map(m => m.content)).toEqual(['first', 'second'])
    expect(room.online.value).toBe('18'); expect(room.connected.value).toBe(false)
  })
  it('keeps the previous room snapshot and ignores stale callbacks after reconnect', () => {
    const { room, socket } = start()
    const oldMessage = socket.onmessage!
    const oldClose = socket.onclose!
    socket.message('{"type":101,"data":{"content":"old"}}')
    room.disconnect(); room.config.room = '2002'
    expect(room.sessionRoom.value).toBe('1001')
    room.connect(); FakeSocket.instances[1]!.open()
    oldMessage({ data: '{"type":101,"data":{"content":"stale"}}' }); oldClose({ wasClean: false, code: 1006, reason: '' })
    vi.runAllTimers()
    expect(room.messages).toHaveLength(0); expect(room.status.value).toBe('connected'); expect(room.sessionRoom.value).toBe('2002')
    room.dispose()
  })
  it('reports handshake failure and remote closure without reconnecting', () => {
    const room = useRoomSession(); room.connect(); FakeSocket.instances[0]!.ended()
    expect(room.status.value).toBe('failed'); expect(room.feedback.value).toContain('连接失败')
    room.connect(); FakeSocket.instances[1]!.open(); FakeSocket.instances[1]!.ended()
    expect(room.status.value).toBe('disconnected'); expect(room.feedbackError.value).toBe(true)
    vi.runAllTimers(); expect(FakeSocket.instances).toHaveLength(2)
  })
  it('surfaces closing errors and stops sends and timers on disposal', () => {
    const { room, socket } = start(); socket.failClose = true; room.disconnect()
    expect(room.feedbackError.value).toBe(true); expect(room.send('no')).toBe(false)
    room.dispose(); expect(vi.getTimerCount()).toBe(0)
  })
})
