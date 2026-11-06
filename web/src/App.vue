<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoomSession } from './useRoomSession'
import { useLocalBackend } from './useLocalBackend'
import LocalBackendPanel from './LocalBackendPanel.vue'

const {
  config, status, feedback, feedbackError, messages, online, likes,
  sessionRoom, url, locked, connected, connect, disconnect, send, like, dispose,
} = useRoomSession()

const backend = useLocalBackend()
const { available: localAvailable, snapshot: localSnapshot, error: localError, logs: localLogs, pending: localPending } = backend
const stoppingSelected = computed(() => {
  const state = localSnapshot.value
  const server = state?.services.find(service => service.id === 'server')
  return !!state && String(config.port).trim() === String(state.port) && !!server?.owned && ((state.busy && state.operation === 'stop') || (state.phase === 'stop_failed' && server.phase !== 'ready'))
})
const draft = ref('')
const composing = ref(false)
const messageList = ref<HTMLElement | null>(null)
const stateLabel = computed(() => ({
  idle: '未连接',
  connecting: '连接中',
  connected: '已连接',
  disconnected: '已断开',
  failed: '连接失败',
})[status.value])
const emptyTitle = computed(() => {
  if (connected.value) return '等待第一条弹幕'
  if (status.value === 'connecting') return '正在连接房间'
  if (status.value === 'disconnected') return '本次会话没有收到弹幕'
  return '连接房间后接收弹幕'
})

function submitDraft() {
  if (!stoppingSelected.value && !composing.value && send(draft.value)) draft.value = ''
}

function onInputKeydown(event: KeyboardEvent) {
  if (event.key !== 'Enter' || event.isComposing || event.keyCode === 229 || composing.value) return
  event.preventDefault()
  submitDraft()
}

watch(() => messages.length, async (length, previousLength) => {
  const list = messageList.value
  if (!list) return
  const wasAtBottom = list.scrollHeight - list.scrollTop - list.clientHeight < 40
  if (length < previousLength || wasAtBottom) {
    await nextTick()
    list.scrollTop = list.scrollHeight
  }
})

onMounted(() => { window.addEventListener('pagehide', dispose); void backend.retry() })
onBeforeUnmount(() => {
  window.removeEventListener('pagehide', dispose)
  dispose()
  backend.dispose()
})
</script>

<template>
  <div class="app-shell">
    <header class="app-header">
      <span class="brand-mark" aria-hidden="true">D</span>
      <span class="brand-name">DanmakuFlow</span>
      <span class="product-label">直播间互动</span>
    </header>

    <LocalBackendPanel v-if="localAvailable" :snapshot="localSnapshot" :error="localError" :logs="localLogs" :pending="localPending" @start="backend.start" @stop="backend.stop" @retry="backend.retry" @logs="backend.loadLogs" />

    <main class="room-layout">
      <div class="stage-column">
        <section class="stage" aria-label="房间信息与视频占位">
          <div class="room-header">
            <div class="room-heading">
              <h1 id="df-title">{{ sessionRoom ? `房间 ${sessionRoom}` : '房间互动' }}</h1>
              <div class="connection-status" :class="{ 'is-connected': connected, 'is-error': status === 'failed' }">
                <span class="status-dot" aria-hidden="true"></span>
                <span>互动连接：</span><span id="df-status-label">{{ stateLabel }}</span>
              </div>
            </div>
            <dl class="room-stats" aria-label="服务端推送的最新统计">
              <div><dt>在线连接</dt><dd id="df-online" data-testid="online-count">{{ online ?? '—' }}</dd></div>
              <div><dt>点赞数</dt><dd id="df-likes" data-testid="like-count">{{ likes ?? '—' }}</dd></div>
            </dl>
          </div>
          <div class="video-placeholder" aria-label="视频画面暂未显示，仅为视觉占位">
            <div class="video-caption"><span>直播画面</span><span>画面未显示</span></div>
            <div class="video-empty">
              <span class="video-symbol" aria-hidden="true"><span></span></span>
              <strong>视频画面暂未显示</strong>
              <p>当前仅展示房间互动</p>
            </div>
          </div>
        </section>

        <section class="connection-panel" aria-labelledby="connection-title">
          <h2 id="connection-title">连接设置</h2>
          <div class="connection-fields">
            <div class="fields">
              <label for="df-port">服务端端口
                <input id="df-port" v-model="config.port" inputmode="numeric" :disabled="locked" autocomplete="off" spellcheck="false">
              </label>
              <label for="df-room-id">房间 ID
                <input id="df-room-id" v-model="config.room" :disabled="locked" autocomplete="off" spellcheck="false">
              </label>
              <div class="uid-field">
                <label for="df-uid">用户 ID
                  <input id="df-uid" v-model="config.uid" inputmode="numeric" :disabled="locked" placeholder="留空自动生成" autocomplete="off" spellcheck="false" aria-describedby="uid-hint">
                </label>
                <p id="uid-hint" class="field-hint">留空自动生成</p>
              </div>
            </div>
            <button id="df-connect" type="button" class="connect-button" :class="{ primary: !locked }" :disabled="status === 'connecting' || (!connected && stoppingSelected)" @click="connected ? disconnect() : connect()">
              {{ status === 'connecting' ? '连接中…' : connected ? '断开连接' : '连接房间' }}
            </button>
          </div>
          <div class="connection-address">
            <span>{{ locked ? '当前连接地址' : '下次连接地址' }}</span>
            <code id="df-url">{{ url }}</code>
          </div>
        </section>
      </div>

      <section class="chat-panel" aria-labelledby="chat-title">
        <header class="chat-header"><h2 id="chat-title">房间弹幕</h2><span>按接收顺序</span></header>
        <div id="df-messages" ref="messageList" class="message-list" role="log" aria-label="房间弹幕" aria-live="off">
          <div v-if="messages.length === 0" class="empty-chat">
            <strong>{{ emptyTitle }}</strong>
            <p>{{ connected ? '收到的弹幕会显示在这里。' : '填写连接信息，开始本次会话。' }}</p>
          </div>
          <div v-for="message in messages" :key="message.key" class="message-row">
            <time class="message-time" aria-label="本地接收时间">{{ message.time }}</time>
            <p class="message-content">{{ message.content }}</p>
          </div>
        </div>

        <div id="df-feedback" class="feedback" :class="{ 'is-error': feedbackError }" :role="feedbackError ? 'alert' : 'status'" aria-live="polite">{{ feedback }}</div>
        <section class="composer" aria-label="发送弹幕">
          <input id="df-message" v-model="draft" class="message-input" aria-label="弹幕内容" placeholder="输入弹幕，按 Enter 发送" :disabled="!connected || stoppingSelected" autocomplete="off" @keydown="onInputKeydown" @compositionstart="composing = true" @compositionend="composing = false">
          <div class="composer-actions">
            <button id="df-send" type="button" class="primary" :disabled="!connected || stoppingSelected" @click="submitDraft">发送</button>
            <button id="df-like" type="button" :disabled="!connected || stoppingSelected" @click="like">点赞</button>
          </div>
          <div class="composer-hints">
            <span>输入恰好为 1 时，提交一次点赞</span>
            <span>{{ !locked && online !== null ? '上次接收的统计 · 已停止更新' : '统计来自服务端推送' }}</span>
          </div>
        </section>
      </section>
    </main>
  </div>
</template>


