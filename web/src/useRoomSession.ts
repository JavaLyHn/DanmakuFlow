import { computed, reactive, ref, shallowReactive } from 'vue'
import { connectionUrl, decodePacket, encodeInput, generateUid } from './protocol'

export interface ChatLine { key: number; content: string; time: string }
type Status = 'idle' | 'connecting' | 'connected' | 'disconnected' | 'failed'

export function useRoomSession() {
  const config = reactive({ port: '8080', room: '1001', uid: '' })
  const status = ref<Status>('idle')
  const feedback = ref('填写连接信息后连接房间。')
  const feedbackError = ref(false)
  const messages = shallowReactive<ChatLine[]>([])
  const online = ref<string | null>(null), likes = ref<string | null>(null)
  const sessionRoom = ref(''), sessionUrl = ref('')
  const connected = computed(() => status.value === 'connected')
  const locked = computed(() => status.value === 'connecting' || connected.value)
  const url = computed(() => {
    if (locked.value) return sessionUrl.value
    const query = new URLSearchParams({ uid: config.uid || '〈自动生成〉', name: config.uid || '〈同用户ID〉', room: config.room })
    return `ws://127.0.0.1:${config.port}/ws?${query}`
  })
  let socket: WebSocket | null = null
  let nextKey = 0
  let pending: ChatLine[] = []
  let timer: ReturnType<typeof setTimeout> | undefined
  function inform(text: string, error = false) { feedback.value = text; feedbackError.value = error }
  function flush() {
    clearTimeout(timer); timer = undefined
    for (const message of pending) messages.push(message)
    pending = []
  }
  function detach(current: WebSocket) {
    current.onopen = null; current.onmessage = null; current.onerror = null; current.onclose = null
    if (socket === current) socket = null
  }
  function disconnect() {
    if (!socket) return
    const current = socket
    detach(current); flush(); status.value = 'disconnected'
    try { current.close(1000); inform('连接已关闭。') }
    catch { inform('关闭连接时发生错误，本次会话已结束。', true) }
  }
  function connect() {
    if (locked.value) return
    let target: string
    try {
      if (!config.uid) config.uid = generateUid()
      target = connectionUrl(config)
    } catch (error) { inform(error instanceof Error ? error.message : '连接参数无效。', true); return }
    flush(); messages.splice(0); online.value = null; likes.value = null
    sessionRoom.value = config.room; sessionUrl.value = target
    status.value = 'connecting'; inform('正在建立连接…')
    let current: WebSocket
    try { current = new WebSocket(target); socket = current }
    catch { status.value = 'failed'; inform('连接失败，请检查连接地址和浏览器网络设置。', true); return }
    let opened = false
    current.onopen = () => {
      if (socket !== current) return
      opened = true; status.value = 'connected'; inform('连接成功，可以发送弹幕或点赞。')
    }
    current.onmessage = event => {
      if (socket !== current || typeof event.data !== 'string') return
      const packet = decodePacket(event.data)
      if (packet?.kind === 'stats') { online.value = packet.online; likes.value = packet.likes }
      else if (packet?.kind === 'message') {
        pending.push({ key: ++nextKey, content: packet.content, time: new Date().toLocaleTimeString('zh-CN', { hour12: false }) })
        if (timer === undefined) timer = setTimeout(flush, 32)
      }
    }
    current.onerror = () => {
      if (socket !== current) return
      inform(opened ? '连接发生错误。' : '连接失败，请检查服务是否启动、端口是否正确。', true)
    }
    current.onclose = event => {
      if (socket !== current) return
      detach(current); flush(); status.value = opened ? 'disconnected' : 'failed'
      const detail = event.reason ? `：${event.reason}` : `（关闭码 ${event.code}）`
      inform(opened ? (event.wasClean ? '服务端已关闭连接' : '连接异常中断') + detail : '连接失败，请检查服务是否启动、端口是否正确。', !opened || !event.wasClean)
    }
  }
  function send(text: string): boolean {
    if (!socket || !connected.value || socket.readyState !== 1) { inform('当前连接不可发送，请重新连接房间。', true); return false }
    try {
      socket.send(encodeInput(text))
      inform(text === '1' ? '已提交点赞操作，等待服务端统计更新。' : '已提交弹幕，等待房间广播。')
      return true
    } catch { inform('发送失败，输入内容已保留。', true); return false }
  }
  function like() { return send('1') }
  function dispose() { disconnect(); flush() }
  return { config, status, feedback, feedbackError, messages, online, likes, sessionRoom, url, locked, connected, connect, disconnect, send, like, dispose }
}
