<script setup>
import { ref, computed, inject, onMounted, onUnmounted } from 'vue'
import { api } from '../api'
import { DONATE_WX, DONATE_ALI } from '../donate'
import { pickKernelFile } from '../sdk'

const notify = inject('notify')
const loadStatus = inject('loadStatus')

const form = ref({ proxy_port: 7890, auto_update_enabled: true, auto_update_hours: 6, auto_switch: false })
// 自动切换订阅的节奏参数（单位：秒），默认值即推荐值
const SW_DEFAULTS = { switch_interval: 45, switch_probe_timeout: 5, switch_cooldown: 180, switch_fail_cooldown: 600 }
const swForm = ref({ ...SW_DEFAULTS })
const swSaved = ref({ ...SW_DEFAULTS })
const swLoaded = ref(false)
const swBusy = ref(false)
const swDirty = computed(() => Object.keys(SW_DEFAULTS).some(k => Number(swForm.value[k]) !== Number(swSaved.value[k])))
const info = ref({})
const cfg = ref('')
const showCfg = ref(false)
const busy = ref(false)
const rules = ref({})

// ===================== 内核版本在线切换 =====================
// 「版本信息」里内核版本右侧的「切换版本」按钮：从 Mihomo 官方仓库在线下载其他版本并启用。
const kModal = ref(false)
const kReleases = ref([])
const kLoading = ref(false)
const kError = ref('')
const kWarning = ref('')
const kFetchedAt = ref(0)
const kShowPre = ref(false)
const kInfo = ref({})
const kStatus = ref({ busy: false })
const kManual = ref('')
const kFileBusy = ref(false)
let kTimer = null

const kActive = computed(() => kInfo.value.active || info.value.mihomo || '')
const kSource = computed(() => kInfo.value.source || info.value.mihomo_source || 'builtin')
const kSourceLabel = computed(() => (kSource.value === 'local' ? '本地文件' : (kSource.value === 'online' ? '在线下载' : '随应用内置')))
const kGoarch = computed(() => kInfo.value.goarch || '')
const kShowList = computed(() => {
  const arch = kGoarch.value
  return (kReleases.value || []).filter((r) => {
    if (!kShowPre.value && r.prerelease) return false
    // 只显示当前架构的内核包：官方 asset 名以 mihomo-linux-<goarch>- 开头
    if (arch && r.asset && !r.asset.includes('-linux-' + arch + '-')) return false
    return true
  })
})
const kPercent = computed(() => {
  const s = kStatus.value || {}
  if (!s.total || s.total <= 0) return 0
  return Math.max(0, Math.min(100, Math.round((Number(s.downloaded || 0) / Number(s.total)) * 100)))
})
const kStatusText = computed(() => {
  const s = kStatus.value || {}
  if (s.error) return '切换失败：' + s.error
  if (!s.busy) return s.stage === 'done' ? (s.message || '已切换完成') : ''
  const map = { queued: '准备中…', downloading: '正在下载内核…', verifying: '正在校验内核…', activating: '正在重启内核…' }
  return s.message || map[s.stage] || '处理中…'
})

function fmtKTime(v) {
  if (!v) return '—'
  const d = new Date(v)
  return isNaN(d.getTime()) ? v : d.toLocaleString()
}
function fmtKDate(v) {
  if (!v) return '—'
  const d = new Date(v)
  return isNaN(d.getTime()) ? v : d.toLocaleDateString()
}
function syncInfoFromKernel(s) {
  if (!s) return
  info.value = { ...info.value, mihomo: s.active, mihomo_source: s.source, mihomo_bundled: s.bundled }
}
function stopKernelPoll() {
  if (kTimer) { clearInterval(kTimer); kTimer = null }
}
function startKernelPoll() {
  if (kTimer) return
  kTimer = setInterval(async () => {
    try {
      const s = await api.get('/api/mihomo/kernel')
      kInfo.value = s || {}
      syncInfoFromKernel(s)
      kStatus.value = (s && s.status) || { busy: false }
      if (!kStatus.value.busy) {
        stopKernelPoll()
        if (kStatus.value.stage === 'done') notify(kStatus.value.message || '内核切换完成')
        else if (kStatus.value.error) notify('内核切换失败：' + kStatus.value.error, true)
        await refreshAfterSwitch()
        loadReleases(false)
      }
    } catch (e) { /* 轮询失败：下次再试 */ }
  }, 900)
}
async function loadKernel() {
  try {
    kInfo.value = await api.get('/api/mihomo/kernel')
    syncInfoFromKernel(kInfo.value)
    if (kInfo.value && kInfo.value.status) kStatus.value = kInfo.value.status
    if (kStatus.value && kStatus.value.busy) startKernelPoll()
  } catch (e) { /* 忽略 */ }
}
async function loadReleases(force) {
  kLoading.value = true
  kError.value = ''
  try {
    const r = await api.get('/api/mihomo/releases' + (force ? '?refresh=1' : ''))
    kReleases.value = r.releases || []
    kWarning.value = r.warning || ''
    kFetchedAt.value = r.fetched_at || 0
    kInfo.value = { ...kInfo.value, arch: r.arch || kInfo.value.arch, goarch: r.goarch || kInfo.value.goarch }
    if (r.active) {
      kInfo.value = { ...kInfo.value, active: r.active, source: r.source, bundled: r.bundled }
      syncInfoFromKernel({ active: r.active, source: r.source, bundled: r.bundled })
    }
  } catch (e) {
    kError.value = e.message
  } finally {
    kLoading.value = false
  }
}
function openKernel() {
  kModal.value = true
  loadKernel()
  if (!kReleases.value.length) loadReleases(false)
}
function closeKernel() {
  if (kStatus.value && kStatus.value.busy) notify('内核正在下载/切换，关闭窗口不会中断任务')
  kModal.value = false
  stopKernelPoll()
}
async function refreshAfterSwitch() {
  try {
    info.value = await api.get('/api/version')
    rememberAppVer(info.value && info.value.app)
  } catch (e) { /* 忽略 */ }
  await loadKernel()
  await loadStatus()
}
async function doSwitch(rel) {
  if (kStatus.value && kStatus.value.busy) { notify('已有切换任务在进行中', true); return }
  kStatus.value = { busy: true, stage: 'queued', version: rel.label || rel.tag, message: '正在准备…' }
  try {
    await api.post('/api/mihomo/switch', { version: rel.tag, asset: rel.asset || '' })
    startKernelPoll()
  } catch (e) {
    kStatus.value = { busy: false, stage: 'error', error: e.message }
    notify(e.message, true)
  }
}
async function manualSwitch() {
  const v = (kManual.value || '').trim()
  if (!/^v\d+\.\d+\.\d+(?:[-.][0-9A-Za-z.]+)?$/.test(v)) {
    notify('版本号格式应类似 v1.19.33', true)
    return
  }
  await doSwitch({ tag: v, downloaded: false, size: 0 })
}
function sourceLabel(s) {
  if (s === 'local') return '本地文件'
  if (s === 'online') return '在线下载'
  return '随应用内置'
}
// 本地文件的版本号可能和某个官方 tag 相同，但并不是「从官方仓库安装的」，
// 因此只有来源为 online 时才把列表项标成当前使用，避免挡住切回同版本官方内核。
function isActiveRelease(rel) {
  return kSource.value === 'online' && rel.label === kActive.value
}
async function pickLocalKernel() {
  if (kStatus.value && kStatus.value.busy) { notify('已有切换任务在进行中', true); return }
  kFileBusy.value = true
  let picked = null
  try {
    picked = await pickKernelFile()
  } catch (e) {
    notify(e.message, true)
    return
  } finally {
    kFileBusy.value = false
  }
  if (!picked || !picked.path) return // 用户取消
  kStatus.value = { busy: true, stage: 'queued', message: '正在读取本地内核…' }
  try {
    await api.post('/api/mihomo/switch', { path: picked.path })
    startKernelPoll()
  } catch (e) {
    kStatus.value = { busy: false, stage: 'error', error: e.message }
    notify(e.message, true)
  }
}
async function restoreBuiltin() {
  if (kStatus.value && kStatus.value.busy) { notify('已有切换任务在进行中', true); return }
  kStatus.value = { busy: true, stage: 'queued', version: '', message: '正在恢复内置内核…' }
  try {
    await api.post('/api/mihomo/switch', { builtin: true })
    startKernelPoll()
  } catch (e) {
    kStatus.value = { busy: false, stage: 'error', error: e.message }
    notify(e.message, true)
  }
}
onUnmounted(stopKernelPoll)

// 代理/订阅两张卡片：「保存」按钮只在值有改动时可点（与自动切换参数卡片一致的脏值判断）
const saved = ref(null)
const proxyDirty = computed(() => !saved.value || Number(form.value.proxy_port) !== Number(saved.value.proxy_port))
const subsDirty = computed(() => !saved.value ||
  !!form.value.auto_update_enabled !== !!saved.value.auto_update_enabled ||
  Number(form.value.auto_update_hours) !== Number(saved.value.auto_update_hours) ||
  !!form.value.auto_switch !== !!saved.value.auto_switch)
function snapForm() {
  saved.value = {
    proxy_port: Number(form.value.proxy_port),
    auto_update_enabled: !!form.value.auto_update_enabled,
    auto_update_hours: Number(form.value.auto_update_hours),
    auto_switch: !!form.value.auto_switch
  }
}

// 「版本信息」里的应用版本：始终显示版本号。取值优先级 = 接口返回值 > 上次记住的值，
// 不再因为内核未运行 / 接口一时取不到就把版本号显示成「—」。
const APPVER_KEY = 'mihomo.appver'
const appVer = ref('')
try { appVer.value = localStorage.getItem(APPVER_KEY) || '' } catch (e) { appVer.value = '' }
function rememberAppVer(v) {
  if (!v) return
  appVer.value = v
  try { localStorage.setItem(APPVER_KEY, v) } catch (e) { /* 忽略：无痕模式下 localStorage 可能不可用 */ }
}

// 发布者：点击「A鱼儿」显示微信号，并支持一键复制
const WECHAT_ID = 'telegram96'
const wechatShow = ref(false)
function toggleWechat() { wechatShow.value = !wechatShow.value }
async function copyWechat() {
  const text = WECHAT_ID
  try {
    if (navigator.clipboard && window.isSecureContext) {
      await navigator.clipboard.writeText(text)
    } else {
      const ta = document.createElement('textarea')
      ta.value = text; ta.style.position = 'fixed'; ta.style.opacity = '0'
      document.body.appendChild(ta); ta.select()
      document.execCommand('copy'); document.body.removeChild(ta)
    }
    notify('微信号已复制：' + text)
  } catch (e) {
    notify('复制失败，请手动选中复制：' + text, true)
  }
}
// 规则集名称 -> 中文说明（顺序与后端 ruleProviderDefs 一致）
const RULE_LABELS = {
  gfw: '被墙域名（走节点）',
  direct: '国内域名（直连）',
  cncidr: '国内 IP 段（直连）',
  lancidr: '局域网 IP 段（直连）',
  private: '私有域名（直连）'
}
const ruleItems = computed(() =>
  Object.keys(RULE_LABELS).map((n) => ({
    name: n, label: RULE_LABELS[n], count: (rules.value.files || {})[n] ?? 0
  }))
)
const rulesOrigin = computed(() => (rules.value.origin === 'online' ? '（已手动更新）' : '（随应用内置）'))
const rulesUpdated = computed(() => {
  const v = rules.value.updated_at
  return v ? new Date(v).toLocaleString() : '—'
})

async function loadRules() {
  try { rules.value = await api.get('/api/rules') } catch (e) { /* 忽略 */ }
}

async function updateRules() {
  busy.value = true
  try {
    const r = await api.post('/api/rules/update', {})
    notify('分流规则已更新（共 ' + (r.total || 0) + ' 条）')
    await loadRules(); await loadStatus()
  } catch (e) {
    notify('规则更新失败：' + e.message, true)
  } finally { busy.value = false }
}

async function load() {
  try {
    const s = await api.get('/api/settings')
    form.value = {
      proxy_port: s.proxy_port,
      auto_update_enabled: !!s.auto_update_enabled,
      auto_update_hours: s.auto_update_hours,
      auto_switch: !!s.auto_switch
    }
    // 参数卡片有未保存的修改时不覆盖，避免「保存设置」把用户正在编辑的值冲掉
    if (!swLoaded.value || !swDirty.value) {
      swForm.value = {
        switch_interval: s.switch_interval,
        switch_probe_timeout: s.switch_probe_timeout,
        switch_cooldown: s.switch_cooldown,
        switch_fail_cooldown: s.switch_fail_cooldown
      }
      swSaved.value = { ...swForm.value }
    }
    if (!saved.value) snapForm()
    swLoaded.value = true
  } catch (e) { notify(e.message, true) }
  try {
    info.value = await api.get('/api/version')
    rememberAppVer(info.value && info.value.app)
  } catch (e) { /* 忽略：版本号仍用上次记住的值显示 */ }
  await loadRules()
}

// 代理设置卡片：只保存端口
async function saveProxy() {
  busy.value = true
  try {
    const r = await api.put('/api/settings', { proxy_port: Number(form.value.proxy_port) })
    notify(r.message || '代理设置已保存')
    if (saved.value) saved.value.proxy_port = Number(form.value.proxy_port)
    if (r.errors && r.errors.length) notify(r.errors.join('；'), true)
    await load(); await loadStatus()
  } catch (e) {
    notify(e.message, true)
  } finally { busy.value = false }
}

// 订阅设置卡片：定时自动更新订阅 + 自动切换订阅
async function saveSubs() {
  busy.value = true
  try {
    const r = await api.put('/api/settings', {
      auto_update_enabled: !!form.value.auto_update_enabled,
      auto_update_hours: Number(form.value.auto_update_hours),
      auto_switch: !!form.value.auto_switch
    })
    notify(r.message || '订阅设置已保存')
    if (saved.value) {
      saved.value.auto_update_enabled = !!form.value.auto_update_enabled
      saved.value.auto_update_hours = Number(form.value.auto_update_hours)
      saved.value.auto_switch = !!form.value.auto_switch
    }
    if (r.errors && r.errors.length) notify(r.errors.join('；'), true)
    await load(); await loadStatus()
  } catch (e) {
    notify(e.message, true)
  } finally { busy.value = false }
}

async function swSave() {
  swBusy.value = true
  try {
    await api.put('/api/settings', {
      switch_interval: Number(swForm.value.switch_interval),
      switch_probe_timeout: Number(swForm.value.switch_probe_timeout),
      switch_cooldown: Number(swForm.value.switch_cooldown),
      switch_fail_cooldown: Number(swForm.value.switch_fail_cooldown)
    })
    notify('自动切换参数已保存')
    swSaved.value = { ...swForm.value }
  } catch (e) {
    notify(e.message, true)
  } finally { swBusy.value = false }
}

function swReset() {
  swForm.value = { ...SW_DEFAULTS }
}

async function rebuild() {
  busy.value = true
  try {
    const r = await api.post('/api/config/rebuild', {})
    notify('配置已重建' + (r.nodes !== undefined ? '（节点 ' + r.nodes + ' 个）' : ''))
    if (r.errors && r.errors.length) notify(r.errors.join('；'), true)
    await loadStatus()
  } catch (e) { notify(e.message, true) } finally { busy.value = false }
}

async function preview() {
  showCfg.value = !showCfg.value
  if (showCfg.value) {
    try { cfg.value = await api.text('/api/config') } catch (e) { cfg.value = '读取失败：' + e.message }
  }
}

onMounted(load)
</script>

<template>
  <!-- ===================== 1. 代理设置 ===================== -->
  <div class="card" id="proxyCard">
    <h2>代理设置</h2>
    <div class="form">
      <label>代理端口（HTTP 与 SOCKS5 混合端口）</label>
      <input v-model="form.proxy_port" type="number" min="1" max="65535" style="width:160px" />
      <div class="muted">保存后会自动<span class="kw kw-act">重建配置</span>并<span class="kw kw-act">重载内核</span>（<span class="kw kw-warn">内核运行中会短暂重启</span>）。</div>
      <div class="row" style="margin-top:14px">
        <button class="primary" :disabled="busy || !proxyDirty" @click="saveProxy">保存代理设置</button>
        <button :disabled="busy" @click="rebuild">重建配置</button>
        <button @click="preview">{{ showCfg ? '隐藏配置' : '预览生成的配置' }}</button>
      </div>
    </div>
    <pre v-if="showCfg" class="codebox">{{ cfg }}</pre>
  </div>

  <!-- ===================== 2. 订阅设置 ===================== -->
  <div class="card" id="subsCard">
    <h2>订阅设置</h2>
    <div class="form">
      <!-- 定时自动更新订阅：标题后紧跟开关 → 介绍 → 显隐参数 -->
      <div class="sw-head">
        <span class="v">定时自动更新订阅</span>
        <div id="swAutoUpdate" class="switch" :class="{ on: form.auto_update_enabled }" role="switch"
             :aria-checked="String(!!form.auto_update_enabled)" tabindex="0"
             @click="form.auto_update_enabled = !form.auto_update_enabled"></div>
      </div>
      <div class="muted">按下面的间隔<span class="kw kw-act">自动重新拉取订阅</span>，保持节点为最新；单个订阅也可以在<span class="kw kw-act">「订阅管理」</span>页手动更新。</div>
      <template v-if="form.auto_update_enabled">
        <label style="margin-top:12px">更新间隔（小时）</label>
        <input id="autoUpdateHours" v-model="form.auto_update_hours" type="number" min="1" max="168" style="width:120px" />
        <div class="muted">范围 <span class="kw kw-val">1–168</span> 小时，默认 <b class="kw kw-val">6</b>。</div>
      </template>

      <!-- 自动切换订阅：标题后紧跟开关 → 介绍 → 显隐参数（参数同属本卡片，不再单独成卡） -->
      <div class="sw-head" style="margin-top:18px">
        <span class="v">自动切换订阅</span>
        <div id="swAutoSwitch" class="switch" :class="{ on: form.auto_switch }" role="switch"
             :aria-checked="String(!!form.auto_switch)" tabindex="0"
             @click="form.auto_switch = !form.auto_switch"></div>
      </div>
      <div class="muted">
        <span class="kw kw-act">开启</span>后，当<b>当前激活订阅的全部节点都<span class="kw kw-danger">超时</span></b>（无法代理）时，自动切换到订阅列表中第一个可用的订阅；
        切换顺序按<span class="kw kw-key">「订阅管理」</span>页卡片的排列顺序（可用 <span class="kw kw-val">↑ ↓</span> 调整），越靠上越优先。
        仅在<span class="kw kw-warn">总开关已打开</span>、<span class="kw kw-warn">内核运行中</span>生效；刚切换过的一段时间内不会再次切换。<span class="kw kw-act">开启</span>后下方会出现节奏参数。
      </div>

      <div v-if="form.auto_switch" id="swParamsBlock" class="sub-block">
        <h3 class="sub-h">自动切换参数</h3>
        <div class="muted"><span class="kw kw-key">「自动切换订阅」</span>的节奏参数，按需调整，单位都是<b>秒</b>。<span class="kw kw-ok">默认值即推荐值</span>，<span class="kw kw-warn">一般不用改</span>。</div>
        <div class="form">
          <label style="margin-top:10px">检测间隔（秒）：每隔多久检查一次当前订阅还能不能用</label>
          <input v-model="swForm.switch_interval" type="number" min="10" max="600" style="width:130px" />
          <div class="muted">范围 <span class="kw kw-val">10–600</span>，默认 <b class="kw kw-val">45</b>。<span class="kw kw-ok">调小＝发现更快</span>，但<span class="kw kw-warn">探测更频繁</span>。</div>

          <label style="margin-top:10px">单节点探测超时（秒）</label>
          <input v-model="swForm.switch_probe_timeout" type="number" min="1" max="30" style="width:130px" />
          <div class="muted">范围 <span class="kw kw-val">1–30</span>，默认 <b class="kw kw-val">5</b>。节点在这段时间内没响应就算它<span class="kw kw-danger">超时</span>。</div>

          <label style="margin-top:10px">切换冷却时间（秒）：两次自动切换之间至少间隔多久</label>
          <input v-model="swForm.switch_cooldown" type="number" min="10" max="3600" style="width:130px" />
          <div class="muted">范围 <span class="kw kw-val">10–3600</span>，默认 <b class="kw kw-val">180</b>（3 分钟）。防止网络抖动导致来回切换。</div>

          <label style="margin-top:10px">全部不可用后的冷却时间（秒）</label>
          <input v-model="swForm.switch_fail_cooldown" type="number" min="30" max="7200" style="width:130px" />
          <div class="muted">范围 <span class="kw kw-val">30–7200</span>，默认 <b class="kw kw-val">600</b>（10 分钟）。<span class="kw kw-danger">所有订阅都用不了</span>时，隔这么久再试一次。</div>

          <div class="row" style="margin-top:14px">
            <button class="primary" :disabled="swBusy || !swDirty" @click="swSave">保存参数</button>
            <button :disabled="swBusy" @click="swReset">填回默认值</button>
          </div>
        </div>
      </div>

      <div class="row" style="margin-top:14px">
        <button class="primary" :disabled="busy || !subsDirty" @click="saveSubs">保存订阅设置</button>
        <span class="muted" v-if="busy">保存中…（<span class="kw kw-warn">端口变化时内核会短暂重启</span>）</span>
      </div>
    </div>
  </div>


  <!-- ===================== 3. 分流规则设置 ===================== -->
  <div class="card" id="rulesCard">
    <h2>分流规则设置</h2>
    <div class="muted">
      <p>规则集随应用内置，作用于<span class="kw kw-key">分流判定</span>：<span class="kw kw-val">内网/局域网</span> → <span class="kw kw-key">被墙域名走节点</span> → <span class="kw kw-ok">国内域名与 IP 直连</span> → <span class="kw kw-key">订阅自带规则</span>。</p>
      <p>规则来自上游公开仓库（{{ rules.source || 'Loyalsoldier/clash-rules' }}）。<span class="kw kw-warn">若规则过期</span>，可在此<b>直接更新，<span class="kw kw-ok">无需升级应用</span></b>。</p>
    </div>
    <table style="margin-top:10px">
      <tbody>
        <tr v-for="it in ruleItems" :key="it.name">
          <th style="width:200px">{{ it.label }}</th>
          <td class="mono">{{ it.count }} 条</td>
        </tr>
        <tr><th>规则总计</th><td class="mono">{{ rules.total || 0 }} 条</td></tr>
        <tr><th>最近更新</th><td class="mono">{{ rulesUpdated }}</td></tr>
      </tbody>
    </table>
    <div class="row" style="margin-top:14px">
      <button class="primary" :disabled="busy" @click="updateRules">更新分流规则</button>
      <button :disabled="busy" @click="loadRules">刷新状态</button>
      <span class="muted" v-if="busy">更新中，可能需要十几秒…</span>
    </div>
  </div>

  <!-- ===================== 4. 版本信息 ===================== -->
  <div class="card" id="verCard">
    <h2>版本信息</h2>
    <table>
      <tbody>
        <tr><th style="width:180px">应用版本</th><td class="mono" id="appVerCell">{{ appVer || '读取中…' }}</td></tr>
        <tr>
          <th>内核版本</th>
          <td>
            <span class="mono" id="kernelVerCell">{{ info.mihomo || '未知' }}</span>
            <span class="badge" :class="info.mihomo_source === 'builtin' ? 'off' : 'on'" style="margin-left:8px">
              {{ sourceLabel(info.mihomo_source) }}
            </span>
            <span class="muted" v-if="info.mihomo_source !== 'builtin'">（内置 {{ info.mihomo_bundled || '—' }}）</span>
            <button class="sm primary" id="switchKernelBtn" style="margin-left:10px" @click="openKernel">切换版本</button>
          </td>
        </tr>
        <tr>
          <th>规则版本</th>
          <td class="mono">{{ info.rules || '—' }}<span class="muted"> {{ rulesOrigin }}</span></td>
        </tr>
        <tr>
          <th>开发者</th>
          <td>
            <a class="link" href="https://github.com/Tianxiaodudou/Mihomo-fpk" target="_blank" rel="noopener">Tianxiaodudou ↗</a>
            <span class="muted">（<span class="kw kw-act">点击打开项目主页</span>）</span>
          </td>
        </tr>
        <tr>
          <th>发布者</th>
          <td id="publisherCell">
            <a id="publisherLink" href="#" role="button" title="点击查看并复制微信号" @click.prevent="toggleWechat">A鱼儿</a>
            <span class="wx-box" v-show="wechatShow">微信号：<b class="mono">{{ WECHAT_ID }}</b>
              <button class="sm primary" id="copyWechatBtn" style="margin-left:8px" @click="copyWechat">一键复制</button></span>
          </td>
        </tr>
      </tbody>
    </table>
    <div class="muted" style="margin-top:10px">
      点「<span class="kw kw-act">切换版本</span>」可在线下载并启用 <span class="kw kw-key">Mihomo 官方仓库</span>发布的其他内核编译变体（v1/v2/v3、go120/go123 等），
      <span class="kw kw-warn">切换时内核会短暂重启</span>，订阅与配置不受影响；也可以随时<span class="kw kw-act">恢复内置版本</span>。
    </div>
  </div>

  <!-- ===================== 5. 数据说明 ===================== -->
  <div class="card" id="dataCard">
    <h2>数据说明</h2>
    <div class="muted">
      <p>· 订阅缓存与运行数据保存在应用的<span class="kw kw-key">私有目录</span>（<span class="kw kw-val">/var/apps/MihomoProxy/var</span>），<span class="kw kw-ok">升级或重装不会丢失</span>。</p>
      <p>· 内核仅监听本机的 <span class="kw kw-val">Unix Socket</span> 控制接口，<span class="kw kw-ok">不对外开放</span>；代理端口默认监听 <span class="kw kw-val">0.0.0.0</span>，请<span class="kw kw-warn">仅在可信局域网内使用</span>。</p>
      <p>· 本应用<span class="kw kw-warn">不修改系统代理设置</span>，<span class="kw kw-key">「显式代理」</span>指需要<span class="kw kw-act">在客户端手动填写</span> NAS 的 <span class="kw kw-val">IP 与端口</span>。</p>
      <p>· <span class="kw kw-act">关闭总开关</span>会<span class="kw kw-warn">立即停止内核</span>，但保留订阅与配置；<span class="kw kw-act">重新开启</span>时会自动重建配置。</p>
    </div>
  </div>

  <!-- ===================== 6. 支持一下（自由自愿打赏）===================== -->
  <div class="card" id="donateCard">
    <!-- 置顶强化：开源号召（比捐赠区更醒目） -->
    <div class="donate-open">
      <div class="donate-open-title">🌐 完全开源 · 欢迎一起来完善</div>
      <div style="line-height:1.8;font-size:14px">
        源码托管在 GitHub：<a class="ghbtn" href="https://github.com/Tianxiaodudou/Mihomo-fpk" target="_blank" rel="noopener">github.com/Tianxiaodudou/Mihomo-fpk ↗</a><br>
        <b>任何人都可以去我的仓库完善、改进、提 issue / PR</b>，
        一起把这个应用做得更好 🙏 有问题也欢迎反馈，我会持续更新。
      </div>
    </div>
    <!-- 捐赠区 -->
    <div class="donate-center">
      <h2 class="donate-h2">☕ 用爱发电 · 支持一下（完全自愿）</h2>
      <p class="muted donate-p">
        本应用<b class="kw kw-ok">完全免费、无广告、无内购、无任何隐藏收费</b>，代码开源（<span class="kw kw-val">GPL-3.0</span>）。
      </p>
      <!-- 强调块：作者自述（醒目，防被一眼跳过） -->
      <div class="donate-note">
        <div style="font-size:15.5px;line-height:1.8">
          💬 说实话，本大叔<b>完全不会写应用、编译应用</b>什么的，
          全靠<b>💰 花钱烧 token 请 AI 帮忙</b>才做出这个应用 😂
        </div>
        <small>（做订阅解析、做分流规则、做这个界面，也是业余时间一点一点磨出来的）</small>
      </div>
      <p class="muted donate-p">
        如果你觉得它好用、帮到了你，<b>愿意的话</b>可以扫码打赏一杯奶茶钱——<br>
        纯属<b class="kw kw-ok">自愿捐赠</b>，金额随意、可随时停止，<b class="kw kw-ok">与任何功能/权限无关</b>：
        <span class="kw kw-warn">打赏不会解锁、不会加速、也不会影响后续使用</span>，你的心意只是让作者更有动力继续维护它
        （毕竟为爱发电已经把饭费烧光啦 🍚😆）。
      </p>
      <div class="donate-qrs">
        <div class="donate-qr">
          <img id="donateImgWx" :src="DONATE_WX" alt="微信收款码">
          <div style="margin-top:6px"><b>微信 · 随意</b></div>
        </div>
        <div class="donate-qr">
          <img id="donateImgAli" :src="DONATE_ALI" alt="支付宝收款码">
          <div style="margin-top:6px"><b>支付宝 · 随意</b></div>
        </div>
      </div>
      <p class="muted" style="margin:4px auto 0">谢谢你的每一份支持 🙏</p>
    </div>
  </div>

  <!-- ===================== 内核版本切换弹窗 ===================== -->
  <Teleport to="body">
    <div v-if="kModal" class="k-mask" @click.self="closeKernel">
      <div class="k-panel" role="dialog" aria-modal="true" aria-label="切换内核版本">
        <div class="k-head">
          <div>
            <h2>切换 Mihomo 内核版本</h2>
            <div class="muted k-sub">
              当前：<b class="mono">{{ kActive || '未知' }}</b>
              <span class="badge" :class="kSource === 'builtin' ? 'off' : 'on'">{{ kSourceLabel }}</span>
              <span class="muted" v-if="kSource !== 'builtin'">（内置 {{ kInfo.bundled || '—' }}）</span>
              <span class="badge off" v-if="kGoarch" title="只显示本机 CPU 架构可用的内核">linux/{{ kGoarch }}</span>
            </div>
          </div>
          <button class="k-x" @click="closeKernel" aria-label="关闭">✕</button>
        </div>

        <div class="k-progress-wrap" v-if="kStatus.busy || kStatus.error || kStatus.stage === 'done'">
          <div class="k-progress" v-if="kStatus.busy"><i :style="{ width: (kStatus.stage === 'downloading' && kPercent) ? kPercent + '%' : '38%' }"></i></div>
          <div class="k-progress-text" :class="{ err: !!kStatus.error }">{{ kStatusText }}</div>
        </div>

        <div class="k-body">
          <div class="k-topline">
            <div class="muted">
              以下编译变体全部来自 Mihomo 官方仓库
              <a class="link" href="https://github.com/MetaCubeX/mihomo/releases" target="_blank" rel="noopener">MetaCubeX/mihomo ↗</a>：
              每条显示完整编译版本（如 <span class="kw kw-val">v2-go123-v1.19.33</span>）与发布日期，不再显示更新日志；
              未标 <span class="kw kw-val">goXXX</span> 后缀的是官方默认 Go 构建。
              <b class="kw kw-ok">只显示当前架构</b> <span class="kw kw-val">linux/{{ kGoarch || '—' }}</span> 的 <span class="kw kw-val">.gz</span> 内核包。
              切换后内核会<span class="kw kw-warn">短暂重启</span>，订阅与配置不受影响。
            </div>
            <div class="k-actions">
              <label class="k-check"><input type="checkbox" v-model="kShowPre" /> 显示预发布</label>
              <button class="sm" :disabled="kStatus.busy || kFileBusy" title="用飞牛文件管理器选择 NAS 上的内核文件" @click="pickLocalKernel">{{ kFileBusy ? '选择中…' : '从文件选择' }}</button>
              <button class="sm" :disabled="kLoading" @click="loadReleases(true)">{{ kLoading ? '刷新中…' : '刷新列表' }}</button>
            </div>
          </div>

          <div class="muted" style="margin-top:8px">
            也可以点「<span class="kw kw-act">从文件选择</span>」，用飞牛文件管理器挑 NAS 上自己下载的内核 ——
            官方 <span class="kw kw-val">.gz</span> 压缩包或已解压的 <span class="kw kw-val">mihomo</span> 二进制都可以，
            会自动识别版本并校验。
          </div>

          <div class="notice" v-if="kWarning" style="margin-top:10px">{{ kWarning }}</div>
          <div class="notice" v-if="kSource === 'local'" style="margin-top:10px">
            当前使用的是 <b>本地文件内核</b>（{{ kActive }}）。下面是从官方仓库在线下载的其他版本，也可以重新点「从文件选择」换一个本地内核。
          </div>
          <div class="errbox" v-if="kError" style="margin-top:10px">{{ kError }}</div>

          <div class="k-manual" v-if="kError">
            <div class="muted">如果列表一直拿不到，可以直接填 Mihomo 官方版本号（只支持官方 <span class="kw kw-val">vX.Y.Z</span>）：</div>
            <div class="row" style="margin-top:6px">
              <input v-model="kManual" class="k-input" placeholder="例如 v1.19.33" @keyup.enter="manualSwitch" />
              <button class="primary sm" :disabled="kStatus.busy" @click="manualSwitch">按此版本下载</button>
            </div>
          </div>

          <div class="k-list">
            <div v-if="kLoading && !kReleases.length" class="empty">正在获取 Mihomo 官方版本列表…</div>
            <div v-else-if="!kShowList.length && !kError" class="empty">没有适用于 linux/{{ kGoarch || '当前架构' }} 的可用版本</div>
            <div v-for="rel in kShowList" :key="rel.asset" class="k-rel" :class="{ active: isActiveRelease(rel) }">
              <div class="k-rel-name">
                <b class="mono">{{ rel.label || rel.tag }}</b>
                <span class="badge on" v-if="isActiveRelease(rel)">当前使用</span>
                <span class="badge warn" v-if="rel.prerelease">预发布</span>
                <span class="badge fast" v-if="rel.downloaded && !isActiveRelease(rel)">已下载</span>
              </div>
              <div class="k-rel-act">
                <span class="muted mono k-date">{{ fmtKDate(rel.published_at) }}</span>
                <button class="primary sm" :disabled="kStatus.busy || isActiveRelease(rel)" @click="doSwitch(rel)">
                  {{ isActiveRelease(rel) ? '使用中' : (rel.downloaded ? '使用此版本' : '下载并使用') }}
                </button>
              </div>
            </div>
          </div>
        </div>

        <div class="k-foot">
          <div class="muted">
            下载的内核保存在应用私有目录，<span class="kw kw-ok">升级应用不会丢失</span>；只保留最近 2 个历史版本。
            <span v-if="kFetchedAt">列表更新于 {{ fmtKTime(kFetchedAt * 1000) }}。</span>
          </div>
          <button class="sm" v-if="kSource !== 'builtin'" :disabled="kStatus.busy" @click="restoreBuiltin">恢复内置版本</button>
        </div>
      </div>
    </div>
  </Teleport>
</template>

<style scoped>
.k-mask {
  position: fixed; inset: 0; z-index: 60; padding: 18px;
  display: flex; align-items: center; justify-content: center;
  background: rgba(3, 10, 20, .62); -webkit-backdrop-filter: blur(4px); backdrop-filter: blur(4px);
}
.k-panel {
  width: min(820px, 100%); max-height: 88vh; display: flex; flex-direction: column;
  background: var(--panel); border: 1px solid var(--border2); border-radius: 16px;
  box-shadow: 0 24px 70px -24px var(--shadow); overflow: hidden;
}
.k-head {
  display: flex; align-items: flex-start; gap: 12px; padding: 16px 18px 12px;
  border-bottom: 1px solid var(--border); background: var(--panel-soft);
}
.k-head h2 { font-size: 16px; margin: 0 0 6px; }
.k-sub { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
.k-x {
  margin-left: auto; width: 30px; height: 30px; padding: 0; border-radius: 8px;
  background: transparent; border: 1px solid transparent; color: var(--muted); font-size: 15px;
}
.k-x:hover { background: var(--hover); color: var(--fg); border-color: var(--border2); }
.k-progress-wrap { padding: 10px 18px 0; }
.k-progress { height: 6px; border-radius: 99px; background: var(--soft); overflow: hidden; }
.k-progress > i {
  display: block; height: 100%; border-radius: 99px;
  background: linear-gradient(90deg, var(--primary), var(--neon2));
  transition: width .35s ease; box-shadow: 0 0 10px -2px var(--primary);
}
.k-progress-text { margin-top: 6px; font-size: 12.5px; color: var(--muted); }
.k-progress-text.err { color: var(--err-fg); }
.k-body { padding: 12px 18px 4px; overflow: auto; }
.k-topline { display: flex; align-items: flex-start; gap: 12px; flex-wrap: wrap; }
.k-topline > .muted { flex: 1 1 420px; line-height: 1.7; }
.k-actions { display: flex; align-items: center; gap: 10px; margin-left: auto; }
.k-check { display: inline-flex; align-items: center; gap: 6px; font-size: 12.5px; color: var(--muted); cursor: pointer; }
.k-manual { margin-top: 10px; padding: 10px 12px; border: 1px dashed var(--border2); border-radius: 10px; background: var(--panel-soft); }
.k-input { padding: 6px 10px; border: 1px solid var(--border2); border-radius: 8px; background: var(--panel); color: var(--fg); font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace; width: 180px; }
.k-input:focus { outline: none; border-color: var(--primary); box-shadow: var(--ring); }
.k-list { display: flex; flex-direction: column; gap: 10px; margin-top: 10px; padding-bottom: 6px; }
.k-rel { display: flex; align-items: center; gap: 12px; flex-wrap: wrap; border: 1px solid var(--border); border-radius: 12px; padding: 9px 14px; background: var(--panel); transition: border-color var(--trans), background-color var(--trans); }
.k-rel:hover { border-color: var(--border2); background: var(--panel-soft); }
.k-rel.active { border-color: var(--active-border); background: var(--accent-soft); box-shadow: inset 0 0 0 1px var(--active-border); }
.k-rel-name { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; font-size: 14px; min-width: 0; }
.k-rel-act { display: flex; align-items: center; gap: 12px; margin-left: auto; }
.k-date { font-size: 12.5px; white-space: nowrap; }
.k-foot {
  display: flex; align-items: center; gap: 12px; flex-wrap: wrap;
  padding: 10px 18px 14px; border-top: 1px solid var(--border); background: var(--panel-soft); font-size: 12.5px;
}
.k-foot > .muted { flex: 1 1 420px; line-height: 1.6; }
@media (max-width: 640px) {
  .k-mask { padding: 0; }
  .k-panel { max-height: 100vh; height: 100%; border-radius: 0; border: none; }
  .k-rel-act { margin-left: 0; width: 100%; justify-content: flex-end; }
}
</style>
