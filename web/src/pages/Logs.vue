<script setup>
import { ref, inject, onMounted, onUnmounted, nextTick } from 'vue'
import { BASE } from '../api'

const notify = inject('notify')
const status = inject('status')

const level = ref('info')
const lines = ref([])
const autoScroll = ref(true)
const paused = ref(false)
const box = ref(null)
let es = null

function fmtLine(raw) {
  try {
    const o = JSON.parse(raw)
    const lv = o.type || o.level || 'info'
    const payload = o.payload || o.message || raw
    const d = new Date()
    const p = (n) => String(n).padStart(2, '0')
    const ts = p(d.getHours()) + ':' + p(d.getMinutes()) + ':' + p(d.getSeconds())
    return { ts, lv, text: String(payload) }
  } catch (e) {
    return { ts: '', lv: 'info', text: raw }
  }
}

function push(raw) {
  if (paused.value) return
  lines.value.push(fmtLine(raw))
  if (lines.value.length > 600) lines.value.splice(0, lines.value.length - 600)
  if (autoScroll.value) {
    nextTick(() => { if (box.value) box.value.scrollTop = box.value.scrollHeight })
  }
}

function connect() {
  close()
  if (!status.value.running) {
    notify('内核未运行，请先开启总开关', true)
    return
  }
  try {
    es = new EventSource(BASE + '/api/logs?level=' + encodeURIComponent(level.value))
  } catch (e) {
    notify('日志连接失败', true)
    return
  }
  es.onmessage = (ev) => push(ev.data)
  es.addEventListener('ready', () => notify('日志已连接（级别：' + level.value + '）'))
  es.onerror = () => {
    if (es && es.readyState === EventSource.CLOSED) notify('日志流已断开', true)
  }
}

function close() {
  if (es) { es.close(); es = null }
}

function clear() { lines.value = [] }

onMounted(connect)
onUnmounted(close)
</script>

<template>
  <div class="card">
    <div class="row between">
      <h2 style="margin:0">内核日志</h2>
      <div class="row">
        <select v-model="level" @change="connect">
          <option value="debug">debug</option>
          <option value="info">info</option>
          <option value="warning">warning</option>
          <option value="error">error</option>
        </select>
        <label class="muted" style="display:flex; align-items:center; gap:4px">
          <input type="checkbox" v-model="autoScroll" style="width:auto" />自动滚动
        </label>
        <label class="muted" style="display:flex; align-items:center; gap:4px">
          <input type="checkbox" v-model="paused" style="width:auto" />暂停
        </label>
        <button class="sm" @click="connect">重连</button>
        <button class="sm" @click="clear">清空</button>
      </div>
    </div>
    <div ref="box" class="codebox" style="height:52vh; overflow:auto; margin-top:12px">
      <div v-for="(l, i) in lines" :key="i" :class="'lv-' + l.lv">
        <span class="muted">{{ l.ts }}</span>
        <b style="margin:0 6px">{{ l.lv }}</b>{{ l.text }}
      </div>
      <div v-if="!lines.length" class="empty">暂无日志输出。</div>
    </div>
  </div>
</template>
