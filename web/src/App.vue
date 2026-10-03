<script setup>
import { ref, onMounted, onUnmounted, provide } from 'vue'
import { api } from './api'
import { setTitle } from './sdk'
import Dashboard from './pages/Dashboard.vue'
import Subs from './pages/Subs.vue'
import Nodes from './pages/Nodes.vue'
import SettingsPage from './pages/Settings.vue'
import Logs from './pages/Logs.vue'

const tabs = [
  { k: 'dash', t: '概览' },
  { k: 'subs', t: '订阅' },
  { k: 'nodes', t: '节点' },
  { k: 'logs', t: '日志' },
  { k: 'settings', t: '设置' }
]
const tab = ref('dash')
const toast = ref(null)
let timer = null
function notify(msg, isErr) {
  toast.value = { msg: String(msg), isErr: !!isErr }
  clearTimeout(timer)
  timer = setTimeout(() => (toast.value = null), 3600)
}
const status = ref({})
async function loadStatus() {
  try {
    status.value = await api.get('/api/status')
    document.title = status.value.running ? '显式代理 · 运行中' : '显式代理'
  } catch (e) { /* 首次加载失败保持静默 */ }
}
provide('notify', notify)
provide('status', status)
provide('loadStatus', loadStatus)

let iv = null
onMounted(() => {
  setTitle('显式代理')
  loadStatus()
  iv = setInterval(loadStatus, 5000)
})
onUnmounted(() => clearInterval(iv))
</script>

<template>
  <div class="app">
    <div class="top">
      <h1>显式代理</h1>
      <span class="badge" :class="status.running ? 'on' : 'off'">
        {{ status.running ? '内核运行中' : '内核已停止' }}
      </span>
      <div class="tabs">
        <button
          v-for="t in tabs"
          :key="t.k"
          class="tab"
          :class="{ active: tab === t.k }"
          @click="tab = t.k"
        >{{ t.t }}</button>
      </div>
    </div>
    <div class="body">
      <Dashboard v-if="tab === 'dash'" @goto="tab = $event" />
      <Subs v-else-if="tab === 'subs'" />
      <Nodes v-else-if="tab === 'nodes'" />
      <Logs v-else-if="tab === 'logs'" />
      <SettingsPage v-else-if="tab === 'settings'" />
    </div>
    <div v-if="toast" class="toast" :class="{ err: toast.isErr }">{{ toast.msg }}</div>
  </div>
</template>
