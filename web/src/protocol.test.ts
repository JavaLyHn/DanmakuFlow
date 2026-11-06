import { describe, expect, it } from 'vitest'
import { connectionUrl, decodePacket, encodeInput, generateUid } from './protocol'

describe('terminal protocol', () => {
  it('preserves raw text and treats only exact 1 as a like', () => {
    expect(JSON.parse(encodeInput('1'))).toEqual({ type: 103, data: null })
    for (const text of ['', ' 1', '1 ', '01', '  hello  ', '<img src=x onerror=alert(1)>']) {
      expect(JSON.parse(encodeInput(text))).toEqual({ type: 101, data: { content: text } })
    }
  })
  it('decodes uint64 statistics without rounding', () => {
    expect(decodePacket('{"type":102,"data":{"online":9007199254740993,"likes":18446744073709551615}}'))
      .toEqual({ kind: 'stats', online: '9007199254740993', likes: '18446744073709551615' })
  })
  it('receives content and ignores invalid or unknown packets', () => {
    expect(decodePacket('{"type":101,"data":{"content":"  hello  "}}')).toEqual({ kind: 'message', content: '  hello  ' })
    for (const raw of ['bad', 'null', '{"type":999}', '{"type":"101","data":{}}', '{"type":102,"data":{"online":-1,"likes":1}}']) expect(decodePacket(raw)).toBeNull()
  })
  it('keeps UID precision and encodes room and name in the fixed local URL', () => {
    const url = new URL(connectionUrl({ port: '8080', uid: '18446744073709551615', room: 'a & b' }))
    expect(url.origin).toBe('ws://127.0.0.1:8080')
    expect(url.searchParams.get('name')).toBe('18446744073709551615')
    expect(url.searchParams.get('room')).toBe('a & b')
    expect(() => connectionUrl({ port: '8080', uid: '18446744073709551616', room: '1' })).toThrow()
    expect(() => connectionUrl({ port: '8080/x', uid: '1', room: '1' })).toThrow()
    expect(() => connectionUrl({ port: '8080', uid: '1', room: '' })).toThrow()
    expect(generateUid()).toMatch(/^\d+$/)
    expect(BigInt(generateUid())).toBeLessThanOrEqual(18446744073709551615n)
  })
})
