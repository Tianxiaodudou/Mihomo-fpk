<script setup>
import { ref, inject, onMounted } from 'vue'
import { api } from '../api'
import { pickSubscriptionFile } from '../sdk'

const notify = inject('notify')
const loadStatus = inject('loadStatus')

const subs = ref([])
const notices = ref([])
const loading = ref(false)
const busyId = ref('')
const pasteShow = ref(false)
const pasteName = ref('')
const pasteText = ref('')
const urlShow = ref(false)
const urlName = ref('')
const urlValue = ref('')

async function load() {
  loading.value = true
  try {
    const r = await api.get('/api/subs')
    subs.value = r.subs || []
    notices.value = r.notices || []
  } catch (e) {
    notify(e.message, true)
  } finally {
    loading.value = false
  }
}

function subSource(s) {
  if (s.url) return s.url
  if (s.path) return s.path
  return '手动粘贴导入'
}

function fmtTime(ts) {
  if (!ts) return '—'
  const d = new Date(ts * 1000)
  const p = (n) => String(n).padStart(2, '0')
  return d.getFullYear() + '-' + p(d.getMonth() + 1) + '-' + p(d.getDate()) + ' ' + p(d.getHours()) + ':' + p(d.getMinutes())
}

async function addByUrl() {
  if (!urlValue.value.trim()) return notify('请填写订阅链接', true)
  try {
    const r = await api.post('/api/subs', { name: urlName.value, url: urlValue.value.trim() })
    notify('已添加并拉取：' + (r.sub ? r.sub.name : ''))
    if (r.errors && r.errors.length) notify(r.errors.join('；'), true)
    urlShow.value = false; urlName.value = ''; urlValue.value = ''
    await load(); await loadStatus()
  } catch (e) { notify(e.message, true) }
}

async function importByPicker() {
  let ret = null
  try {
    ret = await pickSubscriptionFile()
  } catch (e) {
    return notify(e.message || '未选择文件', true)
  }
  let body = null
  if (ret && ret.path) {
    body = { name: fileBaseName(ret.path), path: ret.path }
  } else if (ret && ret.content) {
    body = { name: ret.fileName || '订阅文件', content: ret.content }
  }
  if (!body) return notify('未选择文件', true)
  try {
    const r = await api.post('/api/subs/import', body)
    notify('已导入：' + (r.sub ? r.sub.name : ''))
    if (r.errors && r.errors.length) notify(r.errors.join('；'), true)
    await load(); await loadStatus()
  } catch (e) { notify(e.message, true) }
}

function fileBaseName(p) {
  const s = String(p).split('/').pop() || '订阅文件'
  return s.replace(/\.[^.]+$/, '')
}

function readFileText(f) {
  return new Promise((res, rej) => {
    const fr = new FileReader()
    fr.onload = () => res(String(fr.result))
    fr.onerror = () => rej(new Error('读取文件失败'))
    fr.readAsText(f)
  })
}

async function importPasted() {
  if (!pasteText.value.trim()) return notify('请粘贴订阅内容', true)
  try {
    const r = await api.post('/api/subs/import', { name: pasteName.value || '手动导入', content: pasteText.value })
    notify('已导入：' + (r.sub ? r.sub.name : ''))
    if (r.errors && r.errors.length) notify(r.errors.join('；'), true)
    pasteShow.value = false; pasteName.value = ''; pasteText.value = ''
    await load(); await loadStatus()
  } catch (e) { notify(e.message, true) }
}

async function onDrop(ev) {
  const files = ev.dataTransfer && ev.dataTransfer.files
  if (!files || !files.length) return
  const f = files[0]
  try {
    const text = await readFileText(f)
    const r = await api.post('/api/subs/import', { name: f.name, content: text })
    notify('已导入：' + (r.sub ? r.sub.name : ''))
    if (r.errors && r.errors.length) notify(r.errors.join('；'), true)
    await load(); await loadStatus()
  } catch (e) { notify(e.message, true) }
}

async function updateOne(s) {
  busyId.value = s.id
  try {
    const r = await api.post('/api/subs/' + s.id + '/update', {})
    notify((s.name || '订阅') + ' 更新完成，节点 ' + (r.sub ? r.sub.node_count : 0) + ' 个')
    if (r.errors && r.errors.length) notify(r.errors.join('；'), true)
    await load(); await loadStatus()
  } catch (e) { notify(e.message, true) } finally { busyId.value = '' }
}

async function toggle(s) {
  try {
    await api.put('/api/subs/' + s.id, { enabled: !s.enabled })
    notify((s.name || '订阅') + (s.enabled ? ' 已停用' : ' 已启用'))
    await load(); await loadStatus()
  } catch (e) { notify(e.message, true) }
}

async function rename(s) {
  const cur = s.name || ''
  const v = window.prompt('重命名订阅「' + (cur || s.id) + '」：', cur)
  if (v === null) return
  const name = String(v).trim()
  if (!name) return notify('名称不能为空', true)
  if (name === cur) return
  try {
    await api.put('/api/subs/' + s.id, { name })
    notify('已重命名为「' + name + '」')
    await load()
  } catch (e) { notify(e.message, true) }
}

async function remove(s) {
  if (!confirm('删除订阅「' + (s.name || s.id) + '」？该操作会同时移除其节点。')) return
  try {
    await api.del('/api/subs/' + s.id)
    notify('已删除')
    await load(); await loadStatus()
  } catch (e) { notify(e.message, true) }
}

async function updateAll() {
  busyId.value = 'all'
  notify('正在更新全部订阅…')
  try {
    const r = await api.post('/api/subs/update', {})
    notify('更新完成：可用节点 ' + r.unique + ' 个' + (r.duplicated ? '，去重 ' + r.duplicated + ' 个' : ''))
    if (r.errors && r.errors.length) notify(r.errors.join('；'), true)
    await load(); await loadStatus()
  } catch (e) { notify(e.message, true) } finally { busyId.value = '' }
}

onMounted(load)
</script>

<template>
  <div class="card" @dragover.prevent @drop.prevent="onDrop">
    <div class="row between">
      <h2 style="margin:0">订阅管理</h2>
      <div class="row">
        <button @click="urlShow = !urlShow">添加链接</button>
        <button @click="importByPicker">导入文件</button>
        <button @click="pasteShow = !pasteShow">粘贴内容</button>
        <button class="primary" :disabled="busyId === 'all'" @click="updateAll">全部更新</button>
      </div>
    </div>

    <div v-if="urlShow" class="form" style="margin-top:12px">
      <label>名称（可留空自动生成）</label>
      <input v-model="urlName" placeholder="例如：我的机场" />
      <label>订阅链接</label>
      <input v-model="urlValue" placeholder="https://example.com/sub?token=xxx" />
      <div class="row" style="margin-top:10px">
        <button class="primary" @click="addByUrl">添加并拉取</button>
        <button @click="urlShow = false">取消</button>
      </div>
    </div>

    <div v-if="pasteShow" class="form" style="margin-top:12px">
      <label>名称</label>
      <input v-model="pasteName" placeholder="例如：手动导入" />
      <label>订阅内容（Base64 / Clash YAML / 分享链接，均可）</label>
      <textarea v-model="pasteText" rows="8" placeholder="粘贴订阅内容，支持单个链接、多个链接换行分隔、base64 编码内容或完整 Clash 配置"></textarea>
      <div class="row" style="margin-top:10px">
        <button class="primary" @click="importPasted">导入</button>
        <button @click="pasteShow = false">取消</button>
      </div>
    </div>

    <div class="muted" style="margin-top:10px">提示：也可以把订阅文件（.txt / .yaml / .json / base64）直接拖拽到本页面导入。</div>

    <div class="sub-list" style="margin-top:12px">
      <div v-for="s in subs" :key="s.id" class="sub-card">
        <div class="k">名称</div>
        <div class="v sub-name">{{ s.name || '未命名' }}</div>
        <div class="k" style="margin-top:8px">来源</div>
        <div class="v mono sub-src">{{ subSource(s) }}</div>
        <div class="k" style="margin-top:8px">最近更新</div>
        <div class="v sub-time">{{ fmtTime(s.updated_at) }}</div>
        <div class="row" style="margin-top:10px">
          <button class="sm" :disabled="busyId === s.id" @click="updateOne(s)">更新</button>
          <button class="sm" @click="rename(s)">改名</button>
          <button class="sm" @click="toggle(s)">{{ s.enabled ? '停用' : '启用' }}</button>
          <button class="sm danger" @click="remove(s)">删除</button>
        </div>
        <div v-if="s.last_error" class="errbox" style="margin-top:8px">{{ s.last_error }}</div>
      </div>
      <div v-if="!subs.length" class="empty">还没有订阅，点击上方「添加链接」或「导入文件」开始。</div>
    </div>

    <div v-for="(n, i) in notices" :key="i" class="notice" style="margin-top:10px">{{ n }}</div>
  </div>
</template>
