<script setup lang="ts">
import { computed } from 'vue'
import type { Snapshot } from './useLocalBackend'
const props = defineProps<{ snapshot: Snapshot | null; error: string; logs: string; pending: boolean }>()
const phases: Record<string, string> = { idle: '未启动', unknown: '待确认', starting: '启动中', ready: '已就绪', stopping: '停止中', stopped: '已停止', failed: '失败', stop_failed: '停止失败', partial: '部分运行' }
const phaseLabel = (phase: string) => phases[phase] || '状态待确认'
const startLabel = computed(() => props.snapshot?.busy && props.snapshot.operation === 'start' ? '启动中…' : props.snapshot?.phase === 'ready' ? '后端已就绪' : props.snapshot?.phase === 'failed' ? '重试启动' : '启动后端')
const stopLabel = computed(() => props.snapshot?.busy && props.snapshot.operation === 'stop' ? '停止中…' : props.snapshot?.phase === 'stop_failed' ? '重试停止' : '停止本次启动的服务')
defineEmits<{ start: []; stop: []; retry: []; logs: [id: string] }>()
</script>
<template>
  <section class="local-panel" aria-label="本机后端">
    <div class="local-heading"><h2>本机后端</h2><div class="local-actions">
      <button class="primary" :disabled="pending || !snapshot?.canStart || snapshot?.phase === 'ready'" @click="$emit('start')">{{ startLabel }}</button>
      <button :disabled="pending || !snapshot?.canStop" @click="$emit('stop')">{{ stopLabel }}</button>
      <button v-if="error" @click="$emit('retry')">重新连接启动器</button>
    </div></div>
    <p role="status">{{ snapshot?.progress || '正在连接启动器…' }}</p>
    <p v-if="error || snapshot?.error || snapshot?.configurationError" class="local-error" role="alert">{{ (error || snapshot?.error || snapshot?.configurationError || '').slice(0, 2048) }}</p>
    <ul class="local-services"><li v-for="service in snapshot?.services" :key="service.id">
      <strong>{{ service.label }}</strong><span>{{ phaseLabel(service.phase) }}</span><span>{{ service.owned ? '本次启动' : '非本次启动' }}</span><span>{{ service.detail }}</span>
      <button @click="$emit('logs', service.id)">查看日志</button>
    </li></ul>
    <p class="local-notice">当前 Redis 未启用持久化，停止后点赞和在线统计不会保留；MySQL 中已落库弹幕保留。</p>
    <details v-if="logs" open><summary>日志详情</summary><pre class="local-logs">{{ logs }}</pre></details>
  </section>
</template>

