<script setup>
import { ref, computed, inject, onMounted } from 'vue'
import { api } from '../api'
import { DONATE_WX, DONATE_ALI } from '../donate'

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
        <tr><th>内核版本</th><td class="mono">{{ info.mihomo || '未知' }}</td></tr>
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
</template>
