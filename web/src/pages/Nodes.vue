<script setup>
import { ref, computed, inject, onMounted } from 'vue'
import { api } from '../api'
import { listGroups, primaryGroup, resolveChain, effectiveNode, historyDelay, fmtDelay, delayClass as dcls } from '../proxies'

const notify = inject('notify')
const status = inject('status')

const raw = ref({})
const running = computed(() => raw.value.running !== false && !!status.value.running)
const keyword = ref('')
const testing = ref(false)
const progress = ref('')
const delays = ref({})
const pending = ref({})
// 测速（探测）链接：默认 gstatic 204，可在页面修改并保存（持久化在后端设置里）
const DEFAULT_PROBE = 'http://www.gstatic.com/generate_204'
const probeURL = ref(DEFAULT_PROBE)
const probeSaved = ref(DEFAULT_PROBE)
const savingProbe = ref(false)
const probeDirty = computed(() => (probeURL.value || '').trim() !== probeSaved.value)
// 折叠状态：未记录 = 折叠（每次进入节点页默认全部折叠，只有显式展开过才展开）
const openGroups = ref({})

const proxies = computed(() => raw.value.proxies || {})
const entries = computed(() => Object.values(proxies.value))
// 策略组列表 = 订阅自带的策略组。内核内置的 GLOBAL（全部节点清单，规则不会引用）不显示。
const groups = computed(() => listGroups(proxies.value).filter((g) => g.name !== 'GLOBAL'))
const groupNames = computed(() => groups.value.map((g) => g.name))
// 出口组 = 订阅自己的主策略组（规则 MATCH 指向它）：这个组里选谁，实际出口就是谁。
const exitGroup = computed(() => primaryGroup(proxies.value))
const exitName = computed(() => (exitGroup.value ? exitGroup.value.name : ''))
const chain = computed(() => resolveChain(proxies.value, exitName.value))
const chainText = computed(() => (chain.value.length ? chain.value.join(' → ') : '—'))
const current = computed(() => effectiveNode(proxies.value, chain.value))
function isGroupName(n) {
  return groupNames.value.includes(n)
}
function inExit(g) {
  return !!exitGroup.value && exitGroup.value.name === g.name
}
const TYPE_CN = { Selector: '手动选择', URLTest: '自动测速', Fallback: '故障转移', LoadBalance: '负载均衡', Relay: '链式代理' }
function typeCn(t) {
  return TYPE_CN[t] || t || ''
}

const nodes = computed(() => {
  const names = []
  const seen = new Set()
  entries.value.forEach((e) => {
    if (e && e.type && !groupNames.value.includes(e.name) && !seen.has(e.name)) {
      seen.add(e.name)
      names.push(e)
    }
  })
  return names.sort((a, b) => String(a.name).localeCompare(String(b.name)))
})

// 展开状态：只记录「已展开」的组名（未记录 = 折叠 → 每次进入节点页默认全部折叠）
// 注意：isOpen/toggle 必须同一套语义，否则点击永远展不开（1.0.13 的 bug 就出在这里）
function isOpen(name) {
  return openGroups.value[name] === true
}
function toggle(name) {
  openGroups.value = { ...openGroups.value, [name]: !isOpen(name) }
}
function expandAll(open) {
  const next = {}
  groups.value.forEach((g) => {
    next[g.name] = !!open
  })
  openGroups.value = next
}
// 进入页面时把所有策略组恢复为折叠
function collapseAll() {
  openGroups.value = {}
}

// 某个策略组内的成员（含嵌套的策略组，去重后按搜索词过滤）
function groupNodes(g) {
  const k = keyword.value.trim().toLowerCase()
  const arr = [...new Set(Array.isArray(g.all) ? g.all : [])]
  if (!k) return arr
  return arr.filter((n) => String(n).toLowerCase().includes(k))
}
const visibleGroups = computed(() => groups.value.filter((g) => groupNodes(g).length || !keyword.value.trim()))

// 延迟：本地测速结果优先，其次内核缓存
function delayValue(name) {
  const d = delays.value[name]
  if (d !== undefined) return d
  return historyDelay(proxies.value, name)
}
function delayOf(name) {
  return fmtDelay(delayValue(name))
}
function delayClass(name) {
  return dcls(delayValue(name))
}
function setDelay(name, d) {
  delays.value = { ...delays.value, [name]: d }
}
function seedDelays() {
  const next = { ...delays.value }
  Object.keys(proxies.value).forEach((n) => {
    if (next[n] === undefined) {
      const h = historyDelay(proxies.value, n)
      if (h !== null) next[n] = h
    }
  })
  delays.value = next
}

async function load() {
  try {
    raw.value = await api.get('/api/proxies')
    seedDelays()
  } catch (e) {
    notify(e.message, true)
  }
}

async function loadProbe() {
  try {
    const s = await api.get('/api/settings')
    const u = (s && s.probe_url) || DEFAULT_PROBE
    probeSaved.value = u
    probeURL.value = u
  } catch (e) {
    // 读取设置失败时保持默认值，不打扰用户
  }
}

async function saveProbe() {
  const u = (probeURL.value || '').trim()
  if (!/^https?:\/\/.+/i.test(u)) return notify('测速链接需以 http:// 或 https:// 开头', true)
  savingProbe.value = true
  try {
    const r = await api.put('/api/settings', { probe_url: u })
    const saved = (r && r.settings && r.settings.probe_url) || u
    probeSaved.value = saved
    probeURL.value = saved
    notify('测速链接已保存：' + saved)
  } catch (e) {
    notify(e.message, true)
  } finally {
    savingProbe.value = false
  }
}


// 测速前先确认内核在运行：内核停止时所有延迟必然超时，测了没有意义
async function ensureKernel() {
  let ok = false
  try {
    const st = await api.get('/api/status')
    status.value = st
    ok = !!st.running
  } catch (e) {
    ok = !!status.value.running
  }
  if (!ok) notify('内核未运行，请先到「概览」页打开总开关启动内核，再测速', true)
  return ok
}

async function testNode(name, noCheck) {
  if (!name || pending.value[name]) return -1
  if (!noCheck && !(await ensureKernel())) return -1
  pending.value = { ...pending.value, [name]: true }
  let d = -1
  try {
    const r = await api.raw(
      '/api/proxies/' + encodeURIComponent(name) + '/delay?timeout=5000&url=' + encodeURIComponent(probeURL.value)
    )
    d = r.data && typeof r.data.delay === 'number' && r.data.delay > 0 ? r.data.delay : -1
  } catch (e) {
    d = -1
  }
  setDelay(name, d)
  const p = { ...pending.value }
  delete p[name]
  pending.value = p
  return d
}

// 并发测速池：并发 6，带进度反馈
async function runPool(names, label) {
  const list = [...new Set(names)].filter((n) => n && !groupNames.value.includes(n))
  if (!list.length) return notify('没有可测速的节点', true)
  if (!(await ensureKernel())) return
  testing.value = true
  let done = 0
  progress.value = label + ' 0/' + list.length
  let idx = 0
  const worker = async () => {
    while (idx < list.length) {
      const n = list[idx++]
      await testNode(n, true)
      done += 1
      progress.value = label + ' ' + done + '/' + list.length
    }
  }
  try {
    await Promise.all(Array.from({ length: Math.min(6, list.length) }, worker))
  } finally {
    testing.value = false
    progress.value = ''
    await load()
    const bad = list.filter((n) => {
      const d = delays.value[n]
      return typeof d === 'number' && d < 0
    }).length
    notify('测速完成：共 ' + list.length + ' 个节点' + (bad ? '，' + bad + ' 个超时' : ''))
  }
}

function testGroup(g) {
  if (testing.value) return
  runPool(groupNodes(g), '本组测速')
}
function testAll() {
  if (testing.value) return
  runPool(nodes.value.map((n) => n.name), '全部测速')
}
async function testCurrent() {
  if (!(await ensureKernel())) return
  if (!current.value) return notify('还没有生效节点，请先导入订阅并启动内核', true)
  testNode(current.value, true)
}

// 点节点 = 让「这个策略组」使用该节点（内核里就是该组的 now 切到它）
async function select(g, name) {
  if (!running.value) return notify('请先在「概览」开启总开关', true)
  if (g.now === name) return
  try {
    await api.put('/api/proxies/select', { group: g.name, name })
    notify('「' + g.name + '」已使用 ' + name + (inExit(g) ? '（实际出口随之切换）' : ''))
    await load()
  } catch (e) {
    notify(e.message, true)
  }
}

onMounted(async () => {
  collapseAll()
  await load()
  await loadProbe()
  collapseAll()
})
</script>

<template>
  <div class="card">
    <div class="row between">
      <h2 style="margin:0">策略组与节点</h2>
      <div class="row">
        <span class="muted">测速链接</span>
        <input
          v-model="probeURL"
          style="width:260px"
          placeholder="http://www.gstatic.com/generate_204"
          title="点「测速」时探测的目标地址（默认 http://www.gstatic.com/generate_204）"
        />
        <button class="mini" :disabled="savingProbe || !probeDirty" @click="saveProbe">
          {{ savingProbe ? '保存中…' : '保存' }}
        </button>
        <button :disabled="testing" @click="testAll">{{ testing && progress ? progress : '全部测速' }}</button>
        <button @click="expandAll(true)">全部展开</button>
        <button @click="expandAll(false)">全部收起</button>
        <button @click="load">刷新</button>
      </div>
    </div>

    <div class="row" style="margin-top:8px">
      <span class="muted">
        测速链接：点「测速 / 全部测速 / 本组测速」时探测的目标地址（默认 {{ DEFAULT_PROBE }}，也可改成 https://www.baidu.com
        这类地址）。改完请点右上角「保存」，保存后重启应用、换浏览器都仍然生效。
      </span>
    </div>

    <div class="row" style="margin-top:10px; align-items:center; flex-wrap:wrap">
      <span class="muted">当前出口</span>
      <span class="badge on">{{ chainText }}</span>
      <span class="badge" :class="delayClass(current)">{{ delayOf(current) }}</span>
      <button class="mini" :disabled="testing" @click="testCurrent">
        {{ pending[current] ? '测速中…' : '测速' }}
      </button>
      <span v-if="!running" class="muted">（内核未运行，节点列表为配置文件中的静态数据）</span>
    </div>

    <div class="row" style="margin-top:10px">
      <input v-model="keyword" placeholder="搜索节点" style="width:200px" />
      <span class="muted">共 {{ groups.length }} 个策略组 · {{ nodes.length }} 个节点</span>
    </div>

    <div class="row" style="margin-top:6px">
      <span class="muted">
        这些策略组都来自你的订阅（内核自带的 GLOBAL 组不在此显示）。点某个组里的节点 = 让该组使用该节点；
        实际出口由「出口组」（{{ exitName || '—' }}）里选中的节点决定。
      </span>
    </div>

    <div v-for="g in visibleGroups" :key="g.name" class="group-box">
      <div class="group-head" @click="toggle(g.name)">
        <span class="arrow" :class="{ open: isOpen(g.name) }">▸</span>
        <b>{{ g.name }}</b>
        <span v-if="typeCn(g.type)" class="muted">{{ typeCn(g.type) }}</span>
        <span class="muted">{{ groupNodes(g).length }} 个节点</span>
        <span class="badge on">当前：{{ g.now || '—' }}</span>
        <span v-if="inExit(g)" class="badge">出口组</span>
        <span style="flex:1"></span>
        <button class="mini" :disabled="testing" @click.stop="testGroup(g)">
          {{ testing && progress ? progress : '本组测速' }}
        </button>
      </div>
      <div v-show="isOpen(g.name)" class="group-body">
        <div class="row">
          <span class="muted">
            {{ inExit(g) ? '未匹配到规则的流量走这个组：这里点谁，实际出口就是谁。' : '这个组只有被「' + exitName + '」选中时才影响实际出口。' }}
          </span>
        </div>
        <div class="node-grid">
          <div
            v-for="n in groupNodes(g)"
            :key="n"
            class="node-card"
            :class="{ active: current === n, sel: g.now === n && current !== n, pick: running && !isGroupName(n) }"
            @click="select(g, n)"
          >
            <div class="node-title" :title="n">{{ n }}</div>
            <div class="node-meta">
              <span v-if="isGroupName(n)" class="badge">策略组</span>
              <span v-else class="badge" :class="delayClass(n)">{{ pending[n] ? '测速中…' : delayOf(n) }}</span>
              <span v-if="current === n" class="badge on">生效节点</span>
              <span v-else-if="g.now === n" class="badge">本组当前</span>
            </div>
            <div class="row" style="margin-top:8px">
              <button v-if="!isGroupName(n)" class="mini" :disabled="pending[n] || testing" @click.stop="testNode(n)">
                {{ pending[n] ? '测速中…' : '测速' }}
              </button>
              <button class="mini" :disabled="!running || g.now === n" @click.stop="select(g, n)">
                {{ g.now === n ? '已使用' : '使用' }}
              </button>
            </div>
          </div>
          <div v-if="!groupNodes(g).length" class="empty" style="grid-column:1/-1">没有匹配的节点。</div>
        </div>
      </div>
    </div>

    <div v-if="!groups.length && !nodes.length" class="empty" style="margin-top:12px">
      暂无节点，请先在「订阅」中添加订阅并更新。
    </div>
  </div>
</template>

<style scoped>
.group-box {
  margin-top: 16px;
  border: 1px solid var(--semi-color-border, #e5e6eb);
  border-radius: 10px;
  overflow: hidden;
}
.group-head {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 10px 12px;
  cursor: pointer;
  background: rgba(127, 127, 127, 0.06);
  user-select: none;
}
.group-head:hover {
  background: rgba(127, 127, 127, 0.12);
}
.group-body {
  padding-bottom: 4px;
}
.group-body > .row {
  padding: 8px 12px 0;
}
.arrow {
  transition: transform 0.15s ease;
  display: inline-block;
  font-size: 12px;
}
.arrow.open {
  transform: rotate(90deg);
}
.node-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(210px, 1fr));
  gap: 10px;
  padding: 12px;
}
.node-card {
  border: 1px solid var(--semi-color-border, #e5e6eb);
  border-radius: 10px;
  padding: 10px;
  min-width: 0;
}
.node-card.pick {
  cursor: pointer;
}
.node-card.pick:hover {
  border-color: var(--primary);
}
.node-card.sel {
  border-color: var(--primary);
  box-shadow: inset 0 0 0 1px rgba(22, 104, 220, 0.35);
}
.node-card.active {
  border-color: var(--ok);
  background: rgba(26, 127, 55, 0.1);
  box-shadow: inset 0 0 0 2px rgba(26, 127, 55, 0.55);
}
.node-card.active .node-title::before {
  content: '✓ ';
  color: var(--ok);
  font-weight: 700;
}
.node-title {
  font-weight: 600;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.node-meta {
  display: flex;
  align-items: center;
  gap: 6px;
  margin-top: 6px;
  font-size: 12px;
  flex-wrap: wrap;
}
button.mini {
  padding: 2px 10px;
  font-size: 12px;
  line-height: 20px;
}
</style>
