import { afterEach, expect, it, vi } from 'vitest'
import { useLocalBackend } from './useLocalBackend'

afterEach(() => { vi.restoreAllMocks(); vi.unstubAllGlobals(); vi.useRealTimers() })
const snapshot = { busy: false, operation: '', phase: 'idle', progress: '', canStart: true, canStop: false, port: 8080, services: [] }
it('detects the launcher without starting it and sends the token only on explicit action', async () => {
  const fetcher = vi.fn().mockResolvedValueOnce(new Response(JSON.stringify({ kind: 'danmakuflow-launcher', token: 'secret', port: 8080 }))).mockResolvedValueOnce(new Response(JSON.stringify(snapshot))).mockResolvedValueOnce(new Response(JSON.stringify(snapshot), { status: 202 }))
  vi.stubGlobal('fetch', fetcher)
  const backend = useLocalBackend()
  await backend.retry()
  expect(backend.available.value).toBe(true)
  expect(fetcher).toHaveBeenCalledTimes(2)
  await backend.start()
  expect(fetcher.mock.calls[2][1].headers['X-DanmakuFlow-Token']).toBe('secret')
  backend.dispose()
})
it('hides an unavailable capability and keeps a recognized launcher visible after loss', async () => {
  const fetcher = vi.fn().mockResolvedValueOnce(new Response('', { status: 404 }))
  vi.stubGlobal('fetch', fetcher)
  const backend = useLocalBackend()
  await backend.retry()
  expect(backend.available.value).toBe(false)
  fetcher.mockResolvedValueOnce(new Response(JSON.stringify({ kind: 'danmakuflow-launcher', token: 't', port: 8080 }))).mockResolvedValueOnce(new Response(JSON.stringify(snapshot)))
  await backend.retry()
  fetcher.mockRejectedValueOnce(new Error('offline'))
  await backend.refresh()
  expect(backend.available.value).toBe(true)
  expect(backend.error.value).toContain('offline')
  backend.dispose()
})


it('serializes pending actions and preserves a conflict reason', async () => {
  let resolveAction!: (response: Response) => void
  const fetcher = vi.fn().mockResolvedValueOnce(new Response(JSON.stringify({ kind: 'danmakuflow-launcher', token: 't', port: 8080 }))).mockResolvedValueOnce(new Response(JSON.stringify(snapshot))).mockImplementationOnce(() => new Promise<Response>(resolve => { resolveAction = resolve })).mockResolvedValueOnce(new Response(JSON.stringify(snapshot)))
  vi.stubGlobal('fetch', fetcher)
  const backend = useLocalBackend(); await backend.retry()
  const first = backend.stop(); await backend.stop()
  expect(fetcher).toHaveBeenCalledTimes(3)
  resolveAction(new Response(JSON.stringify({ error: 'stop failed' }), { status: 409 }))
  await first
  expect(backend.error.value).toContain('stop failed')
  fetcher.mockResolvedValueOnce(new Response(JSON.stringify(snapshot), { status: 202 }))
  await backend.stop()
  expect(backend.error.value).toBe('')
  backend.dispose()
})
it('does not update or reschedule after disposal with requests in flight', async () => {
  vi.useFakeTimers()
  let resolveStatus!: (response: Response) => void
  const fetcher = vi.fn().mockResolvedValueOnce(new Response(JSON.stringify({ kind: 'danmakuflow-launcher', token: 't', port: 8080 }))).mockResolvedValueOnce(new Response(JSON.stringify(snapshot))).mockImplementationOnce(() => new Promise<Response>(resolve => { resolveStatus = resolve }))
  vi.stubGlobal('fetch', fetcher)
  const backend = useLocalBackend(); await backend.retry()
  const request = backend.refresh(); backend.dispose()
  resolveStatus(new Response(JSON.stringify({ ...snapshot, progress: 'late' })))
  await request; await vi.advanceTimersByTimeAsync(10000)
  expect(backend.snapshot.value?.progress).toBe('')
  expect(fetcher).toHaveBeenCalledTimes(3)
})
it('caps logs, rejects unknown IDs, and refreshes a restarted launcher token', async () => {
  const fetcher = vi.fn().mockResolvedValueOnce(new Response(JSON.stringify({ kind: 'danmakuflow-launcher', token: 'old', port: 8080 }))).mockResolvedValueOnce(new Response(JSON.stringify(snapshot))).mockResolvedValueOnce(new Response(JSON.stringify({ text: 'x'.repeat(9000) }))).mockResolvedValueOnce(new Response(JSON.stringify({ kind: 'danmakuflow-launcher', token: 'new', port: 8080 }))).mockResolvedValueOnce(new Response(JSON.stringify(snapshot))).mockResolvedValueOnce(new Response(JSON.stringify(snapshot), { status: 202 }))
  vi.stubGlobal('fetch', fetcher)
  const backend = useLocalBackend(); await backend.retry(); await backend.loadLogs('../bad')
  expect(fetcher).toHaveBeenCalledTimes(2)
  await backend.loadLogs('mysql'); expect(backend.logs.value.length).toBe(8192)
  await backend.retry(); await backend.start()
  expect(fetcher.mock.calls[5][1].headers['X-DanmakuFlow-Token']).toBe('new')
  backend.dispose()
})
it('times out a stalled request and permits retry', async () => {
  vi.useFakeTimers()
  const fetcher = vi.fn().mockResolvedValueOnce(new Response(JSON.stringify({ kind: 'danmakuflow-launcher', token: 't', port: 8080 }))).mockResolvedValueOnce(new Response(JSON.stringify(snapshot))).mockImplementationOnce((_url, options) => new Promise((_resolve, reject) => options.signal.addEventListener('abort', () => reject(new Error('aborted'))))).mockResolvedValueOnce(new Response(JSON.stringify(snapshot)))
  vi.stubGlobal('fetch', fetcher)
  const backend = useLocalBackend(); await backend.retry()
  const request = backend.start(); await vi.advanceTimersByTimeAsync(5000); await request
  expect(backend.pending.value).toBe(false)
  expect(backend.error.value).toBeTruthy()
  backend.dispose()
})
