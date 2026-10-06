<script setup>
import { ref, computed, inject, onMounted } from 'vue'
import { api } from '../api'

const notify = inject('notify')
const loadStatus = inject('loadStatus')

const form = ref({ proxy_port: 7890, auto_update_enabled: true, auto_update_hours: 6, auto_switch: false })
const info = ref({})
const cfg = ref('')
const showCfg = ref(false)
const busy = ref(false)
const rules = ref({})

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
  } catch (e) { notify(e.message, true) }
  try { info.value = await api.get('/api/version') } catch (e) { /* 忽略 */ }
  await loadRules()
}

async function save() {
  busy.value = true
  try {
    const r = await api.put('/api/settings', {
      proxy_port: Number(form.value.proxy_port),
      auto_update_enabled: !!form.value.auto_update_enabled,
      auto_update_hours: Number(form.value.auto_update_hours),
      auto_switch: !!form.value.auto_switch
    })
    notify(r.message || '设置已保存')
    if (r.errors && r.errors.length) notify(r.errors.join('；'), true)
    await load(); await loadStatus()
  } catch (e) {
    notify(e.message, true)
  } finally { busy.value = false }
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
  <div class="card">
    <h2>代理设置</h2>
    <div class="form">
      <label>代理端口（HTTP 与 SOCKS5 混合端口）</label>
      <input v-model="form.proxy_port" type="number" min="1" max="65535" style="width:160px" />
      <div class="muted">保存后会自动重建配置并重载内核（内核运行中会短暂重启）。</div>

      <label style="margin-top:12px">
        <input type="checkbox" v-model="form.auto_update_enabled" style="width:auto; margin-right:6px" />
        定时自动更新订阅
      </label>
      <label>更新间隔（小时）</label>
      <input v-model="form.auto_update_hours" type="number" min="1" max="168" style="width:120px" />

      <label style="margin-top:12px">
        <input type="checkbox" v-model="form.auto_switch" style="width:auto; margin-right:6px" />
        自动切换订阅
      </label>
      <div class="muted">
        开启后，当<b>当前激活订阅的全部节点都超时</b>（无法代理）时，自动切换到订阅列表中第一个可用的订阅；
        切换顺序按「订阅管理」页卡片的排列顺序（可用 ↑ ↓ 调整），越靠上越优先。
        仅在总开关已打开、内核运行中生效；刚切换过的 3 分钟内不会再次切换。
      </div>

      <div class="row" style="margin-top:14px">
        <button class="primary" :disabled="busy" @click="save">保存设置</button>
        <button :disabled="busy" @click="rebuild">重建配置</button>
        <button @click="preview">{{ showCfg ? '隐藏配置' : '预览生成的配置' }}</button>
      </div>
    </div>
    <pre v-if="showCfg" class="codebox">{{ cfg }}</pre>
  </div>

  <div class="card">
    <h2>版本信息</h2>
    <table>
      <tbody>
        <tr><th style="width:180px">应用版本</th><td class="mono">{{ info.app || '—' }}</td></tr>
        <tr><th>内核版本</th><td class="mono">{{ info.mihomo || '未运行' }}</td></tr>
        <tr>
          <th>规则版本</th>
          <td class="mono">{{ info.rules || '—' }}<span class="muted"> {{ rulesOrigin }}</span></td>
        </tr>
      </tbody>
    </table>
  </div>

  <div class="card">
    <h2>分流规则</h2>
    <div class="muted">
      <p>规则集随应用内置，作用于分流判定：内网/局域网 → 被墙域名走节点 → 国内域名与 IP 直连 → 订阅自带规则。</p>
      <p>规则来自上游公开仓库（{{ rules.source || 'Loyalsoldier/clash-rules' }}）。若规则过期，可在此<b>直接更新，无需升级应用</b>。</p>
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

  <div class="card">
    <h2>数据与说明</h2>
    <div class="muted">
      <p>· 订阅缓存与运行数据保存在应用的私有目录（/var/apps/MihomoProxy/var），升级或重装不会丢失。</p>
      <p>· 内核仅监听本机的 Unix Socket 控制接口，不对外开放；代理端口默认监听 0.0.0.0，请仅在可信局域网内使用。</p>
      <p>· 本应用不修改系统代理设置，「显式代理」指需要在客户端手动填写 NAS 的 IP 与端口。</p>
      <p>· 关闭总开关会立即停止内核，但保留订阅与配置；重新开启时会自动重建配置。</p>
    </div>
  </div>
</template>
