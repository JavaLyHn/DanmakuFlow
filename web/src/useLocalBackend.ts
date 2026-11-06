import { ref } from 'vue'

export interface Service { id: string; label: string; phase: string; owned: boolean; detail?: string }
export interface Snapshot { busy: boolean; operation: string; phase: string; progress: string; error?: string; configurationError?: string; canStart: boolean; canStop: boolean; port: number; services: Service[] }

export function useLocalBackend() {
  const available = ref(false)
  const snapshot = ref<Snapshot | null>(null)
  const error = ref('')
  const logs = ref('')
  const pending = ref(false)
  let token = ''
  let disposed = false
  let timer: ReturnType<typeof setTimeout> | undefined
  const controllers = new Set<AbortController>()
  async function request(url: string, options: RequestInit = {}) {
    const controller = new AbortController()
    controllers.add(controller)
    let timeout: ReturnType<typeof setTimeout> | undefined
    try {
      return await Promise.race([
        fetch(url, { ...options, signal: controller.signal }).then(async response => {
          const body = await response.text()
          return new Response(body, { status: response.status, statusText: response.statusText, headers: response.headers })
        }),
        new Promise<never>((_, reject) => { timeout = setTimeout(() => { controller.abort(); reject(new Error('请求超时')) }, 5000) }),
      ])
    } finally { clearTimeout(timeout); controllers.delete(controller) }
  }
  const message = (e: unknown) => e instanceof Error ? e.message : String(e)
  function schedule() {
    if (!disposed && available.value) { clearTimeout(timer); timer = setTimeout(async () => { await refresh(); schedule() }, 2000) }
  }
  async function read(response: Response) {
    if (!response.ok) {
      let detail = `HTTP ${response.status}`
      try { detail = (await response.json()).error || detail } catch { /* no JSON error */ }
      throw new Error(detail)
    }
    return response.json()
  }
  async function refresh() {
    try { const data = await read(await request('/api/local/status', { cache: 'no-store' })); if (!disposed) { snapshot.value = data; error.value = '' } }
    catch (e) { if (!disposed) error.value = `启动器连接失败：${message(e)}` }
  }
  async function retry() {
    if (disposed) return
    clearTimeout(timer)
    try {
      const response = await request('/api/local/session', { cache: 'no-store' })
      if (!response.ok) { if (available.value) throw new Error(`HTTP ${response.status}`); return }
      const session = await response.json()
      if (session.kind !== 'danmakuflow-launcher' || typeof session.token !== 'string' || session.port !== 8080) {
        if (available.value) throw new Error('启动器响应无效')
        return
      }
      if (disposed) return
      token = session.token; available.value = true
      await refresh(); schedule()
    } catch (e) { if (available.value && !disposed) error.value = `启动器连接失败：${message(e)}` }
  }
  async function action(name: 'start' | 'stop') {
    if (pending.value || !token || disposed) return
    pending.value = true
    clearTimeout(timer)
    try {
      const data = await read(await request(`/api/local/${name}`, { method: 'POST', headers: { 'X-DanmakuFlow-Token': token } }))
      if (disposed) return
      snapshot.value = data
      error.value = ''
      schedule()
    } catch (e) { if (!disposed) { error.value = message(e); await refreshAfterError() } }
    finally { if (!disposed) { pending.value = false; schedule() } }
  }
  async function refreshAfterError() {
    const saved = error.value
    await refresh()
    if (!disposed && !error.value) error.value = saved
  }
  async function loadLogs(id: string) {
    if (!['mysql', 'redis', 'kafka', 'consumer', 'server'].includes(id) || !token || disposed) return
    try { const data = await read(await request(`/api/local/logs/${id}`, { headers: { 'X-DanmakuFlow-Token': token }, cache: 'no-store' })); if (!disposed) logs.value = String(data.text).slice(-8192) }
    catch (e) { if (!disposed) logs.value = message(e).slice(-8192) }
  }
  function dispose() { disposed = true; clearTimeout(timer); for (const controller of controllers) controller.abort(); controllers.clear() }
  return { available, snapshot, error, logs, pending, retry, refresh, start: () => action('start'), stop: () => action('stop'), loadLogs, dispose }
}





