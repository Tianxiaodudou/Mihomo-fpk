<script setup>
import { ref, inject, onMounted } from 'vue'
import { api } from '../api'
import { pickSubscriptionFile } from '../sdk'

const notify = inject('notify')
const loadStatus = inject('loadStatus')

const subs = ref([])
const notices = ref([])
const activeId = ref('')
const autoSwitch = ref(false)
const switchInfo = ref('')
const switchAt = ref(0)
const switchErr = ref(false)
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
    activeId.value = r.active_id || ''
    autoSwitch.value = !!r.auto_switch
    switchInfo.value = r.switch_info || ''
    switchAt.value = r.switch_at || 0
    switchErr.value = !!r.switch_err
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

async function updateOne(s) {
  busyId.value = s.id
  try {
    const r = await api.post('/api/subs/' + s.id + '/update', {})
    notify((s.name || '订阅') + ' 更新完成，节点 ' + (r.sub ? r.sub.node_count : 0) + ' 个')
    if (r.errors && r.errors.length) notify(r.errors.join('；'), true)
    await load(); await loadStatus()
  } catch (e) { notify(e.message, true) } finally { busyId.value = '' }
}

// 激活 / 取消激活：同一时间只允许一个订阅处于激活状态
async function activate(s, on) {
  try {
    const r = await api.post('/api/subs/' + s.id + '/activate', { active: on })
    notify(on ? '「' + (s.name || s.id) + '」已激活，节点列表显示该订阅的节点' : '「' + (s.name || s.id) + '」已取消激活')
    await load(); await loadStatus()
  } catch (e) { notify(e.message, true) }
}

// 排序：与相邻卡片交换位置；越靠上越优先成为自动切换的备选
async function move(index, delta) {
  const arr = subs.value.slice()
  const j = index + delta
  if (j < 0 || j >= arr.length) return
  const t = arr[index]; arr[index] = arr[j]; arr[j] = t
  subs.value = arr
  try {
    await api.post('/api/subs/reorder', { ids: arr.map((x) => x.id) })
    notify('排序已保存：' + arr.map((x) => x.name || x.id).join(' → '))
  } catch (e) { notify(e.message, true); await load() }
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
  <div class="card">
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

    <div class="muted fmt" style="margin-top:14px">
      <div class="fmt-head"><b>支持的订阅 / 配置文件格式</b>（「添加链接」「导入文件」「粘贴内容」三种方式都会自动识别，不需要手动选格式）：</div>
      <ul>
        <li><b>Clash / Mihomo YAML</b>（.yaml / .yml / .conf）：含 <span class="mono">proxies</span> 与 <span class="mono">proxy-groups</span> 的完整配置。应用只取其中的节点与策略组，分流规则用本应用自己的（订阅自带 rules 不生效）。</li>
        <li><b>分享链接（明文）</b>（.txt / .list，一行一个，也支持空格分隔）：<span class="mono">ss://</span>、<span class="mono">ssr://</span>、<span class="mono">vmess://</span>、<span class="mono">vless://</span>、<span class="mono">trojan://</span>、<span class="mono">hysteria://</span>、<span class="mono">hysteria2://</span>、<span class="mono">hy2://</span>、<span class="mono">tuic://</span>。</li>
        <li><b>整体 Base64 编码的分享链接</b>：机场订阅「复制订阅」得到的那串字母数字，解码后就是上面的分享链接列表；标准 / URL-safe、带不带 <span class="mono">=</span> 填充都能识别。</li>
        <li><b>JSON 节点数组</b>（.json / .ini / .base64）：形如 <span class="mono">[{"type":"ss","server":"1.2.3.4","port":8388,...}]</span> 的 outbound 列表。</li>
        <li><b>订阅链接（URL）</b>：用「添加链接」填入 http/https 地址，应用会自行下载并识别上述任意格式。</li>
      </ul>
      <div class="fmt-foot">识别不出来时会提示「无法识别订阅格式」；文件建议使用 UTF-8 编码。单个订阅节点太多时可先用「全部更新」验证可用性。</div>
    </div>

    <div class="muted" style="margin-top:10px">
      同一时间只有一个订阅处于激活状态，节点列表只显示激活订阅的节点。用卡片右上角的 ↑ ↓ 调整顺序，越靠上越优先成为「自动切换订阅」的备选。
    </div>
    <div v-if="autoSwitch && switchInfo" class="notice" :class="{ errbox: switchErr }" style="margin-top:8px">
      自动切换：{{ switchInfo }}<span v-if="switchAt" class="muted">（{{ fmtTime(switchAt) }}）</span>
    </div>

    <div class="sub-list" style="margin-top:12px">
      <div v-for="(s, i) in subs" :key="s.id" class="sub-card" :class="{ 'sub-active': s.id === activeId }">
        <div class="row between">
          <div class="row">
            <span class="sub-idx">{{ i + 1 }}</span>
            <span class="v sub-name">{{ s.name || '未命名' }}</span>
            <span v-if="s.id === activeId" class="badge-on">已激活</span>
          </div>
          <div class="row">
            <button class="sm" :disabled="i === 0" title="上移（更优先成为备选）" @click="move(i, -1)">↑</button>
            <button class="sm" :disabled="i === subs.length - 1" title="下移" @click="move(i, 1)">↓</button>
          </div>
        </div>
        <div class="k" style="margin-top:8px">来源</div>
        <div class="v mono sub-src">{{ subSource(s) }}</div>
        <div class="k" style="margin-top:8px">最近更新</div>
        <div class="v sub-time">{{ fmtTime(s.updated_at) }}</div>
        <div class="row" style="margin-top:10px">
          <button class="sm" :class="{ primary: s.id !== activeId }" @click="activate(s, s.id !== activeId)">{{ s.id === activeId ? '取消激活' : '激活' }}</button>
          <button class="sm" :disabled="busyId === s.id" @click="updateOne(s)">更新</button>
          <button class="sm" @click="rename(s)">改名</button>
          <button class="sm danger" @click="remove(s)">删除</button>
        </div>
        <div v-if="s.last_error" class="errbox" style="margin-top:8px">{{ s.last_error }}</div>
      </div>
      <div v-if="!subs.length" class="empty">还没有订阅，点击上方「添加链接」或「导入文件」开始。</div>
    </div>

    <div v-for="(n, i) in notices" :key="i" class="notice" style="margin-top:10px">{{ n }}</div>
  </div>
</template>
