<script setup>
import { ref, inject, computed, onMounted, onUnmounted } from 'vue'
import { api } from '../api'
import { primaryGroup, resolveChain, effectiveNode, historyDelay, fmtDelay, delayClass } from '../proxies'

const emit = defineEmits(['goto'])
const notify = inject('notify')
const status = inject('status')
const loadStatus = inject('loadStatus')

const busy = ref(false)
const copied = ref('')
const testing = ref(false)
const proxies = ref({})
const liveDelay = ref(null)

const chain = computed(() => resolveChain(proxies.value, (primaryGroup(proxies.value) || {}).name))
const chainText = computed(() => (chain.value.length ? chain.value.join(' → ') : '—'))
const currentNode = computed(() => effectiveNode(proxies.value, chain.value))
const nodeDelay = computed(() => (liveDelay.value !== null ? liveDelay.value : historyDelay(proxies.value, currentNode.value)))

async function loadProxies() {
  if (!status.value.running) {
    proxies.value = {}
    return
  }
  try {
    proxies.value = await api.get('/api/proxies')
  } catch (e) {
    /* 内核刚起/刚停时可能短暂失败，忽略 */
  }
}

async function testCurrent() {
  if (testing.value || !currentNode.value) return
  testing.value = true
  try {
    const r = await api.raw(
      '/api/proxies/' + encodeURIComponent(currentNode.value) + '/delay?timeout=5000&url=' + encodeURIComponent('http://www.gstatic.com/generate_204')
    )
    const d = r.data && typeof r.data.delay === 'number' && r.data.delay > 0 ? r.data.delay : -1
    liveDelay.value = d
    notify(d > 0 ? '当前节点延迟 ' + d + ' ms' : '当前节点测速超时', d < 0)
  } catch (e) {
    liveDelay.value = -1
    notify(e.message, true)
  } finally {
    testing.value = false
  }
}

const siteList = [
  { id: 'github', label: 'GitHub', url: 'https://github.com/' },
  { id: 'google', label: '谷歌', url: 'https://www.google.com/generate_204' },
  { id: 'bilibili', label: '哔哩哔哩', url: 'https://www.bilibili.com/' },
  { id: 'baidu', label: '百度', url: 'https://www.baidu.com/' },
]
const siteResult = ref({})
const testingSite = ref('')

async function testSite(site) {
  if (!status.value.running || !currentNode.value || testingSite.value) return
  testingSite.value = site.id
  try {
    const r = await api.raw(
      '/api/proxies/' + encodeURIComponent(currentNode.value) + '/delay?timeout=5000&url=' + encodeURIComponent(site.url)
    )
    const d = r.data && typeof r.data.delay === 'number' && r.data.delay > 0 ? r.data.delay : -1
    const next = Object.assign({}, siteResult.value)
    next[site.id] = d
    siteResult.value = next
    notify(site.label + '：' + (d > 0 ? d + ' ms' : '测试超时'), d < 0)
  } catch (e) {
    const next = Object.assign({}, siteResult.value)
    next[site.id] = -1
    siteResult.value = next
    notify(site.label + '：' + e.message, true)
  } finally {
    testingSite.value = ''
  }
}

async function testSites() {
  for (const site of siteList) {
    if (!status.value.running || !currentNode.value) break
    await testSite(site)
  }
}

let timer = null
onMounted(async () => {
  await loadStatus()
  await loadProxies()
  timer = setInterval(() => {
    if (!busy.value) loadProxies()
  }, 15000)
})
onUnmounted(() => {
  if (timer) clearInterval(timer)
})

const lanAddr = computed(() => {
  const host = location.hostname || 'NAS-IP'
  return host + ':' + (status.value.port || 7890)
})

// 等待内核状态变成期望值（内核启动是异步的，需要等它真正生效）
async function waitRunning(expect, tries = 24, gap = 500) {
  for (let i = 0; i < tries; i++) {
    await new Promise((r) => setTimeout(r, gap))
    await loadStatus()
    if (!!status.value.running === expect) return true
  }
  return false
}

async function togglePower() {
  if (busy.value) return
  busy.value = true
  try {
    const on = !status.value.running
    const r = await api.put('/api/power', { enable: on })
    notify(r.message || (on ? '内核已启动' : '内核已关闭'))
    if (on) {
      // 总开关打开：等内核生效后立刻刷新「当前正在使用的节点」
      liveDelay.value = null
      const ok = await waitRunning(true)
      await loadProxies()
      const cur = currentNode.value
      if (ok && cur) notify('当前节点：' + cur)
      else if (!ok) notify('内核启动较慢，正在后台继续刷新当前节点', true)
    } else {
      await loadStatus()
      proxies.value = {}
      liveDelay.value = null
    }
  } catch (e) {
    notify(e.message, true)
  } finally {
    busy.value = false
  }
}

async function updateAll() {
  if (busy.value) return
  busy.value = true
  notify('正在更新订阅…')
  try {
    const r = await api.post('/api/subs/update', {})
    let msg = '订阅更新完成：可用节点 ' + r.unique + ' 个'
    if (r.duplicated) msg += '，去重 ' + r.duplicated + ' 个'
    notify(msg)
    if (r.errors && r.errors.length) notify(r.errors.join('；'), true)
    await loadStatus()
  } catch (e) {
    notify(e.message, true)
  } finally {
    busy.value = false
  }
}

async function copy(text, tag) {
  try {
    if (navigator.clipboard && window.isSecureContext) {
      await navigator.clipboard.writeText(text)
    } else {
      const ta = document.createElement('textarea')
      ta.value = text
      document.body.appendChild(ta)
      ta.select()
      document.execCommand('copy')
      document.body.removeChild(ta)
    }
    copied.value = tag
    setTimeout(() => (copied.value = ''), 1800)
  } catch (e) {
    notify('复制失败，请手动选择文本', true)
  }
}

function fmtTime(ts) {
  if (!ts) return '—'
  const d = new Date(ts * 1000)
  const p = (n) => String(n).padStart(2, '0')
  return d.getFullYear() + '-' + p(d.getMonth() + 1) + '-' + p(d.getDate()) + ' ' + p(d.getHours()) + ':' + p(d.getMinutes())
}

</script>

<template>
  <div class="card">
    <div class="row between">
      <div class="row">
        <div class="switch" :class="{ on: status.running }" @click="togglePower"></div>
        <div>
          <div><b>总开关</b>
            <span class="badge" :class="status.running ? 'on' : 'off'">{{ status.running ? '已开启' : '已关闭' }}</span>
          </div>
          <div class="muted">开启后内核开始对外提供代理服务；关闭立即停止，不影响已保存的配置。</div>
        </div>
      </div>
      <button class="primary" :disabled="busy" @click="updateAll">立即更新全部订阅</button>
    </div>
    <div v-if="status.last_error" class="errbox" style="margin-top:12px">内核提示：{{ status.last_error }}</div>
    <div v-for="(n, i) in (status.notices || [])" :key="i" class="notice" style="margin-top:12px">{{ n }}</div>
  </div>

  <div class="card">
    <h2>代理入口</h2>
    <div class="stat-grid">
      <div class="stat">
        <div class="k">局域网地址（HTTP / SOCKS5 同端口混合）</div>
        <div class="v mono">{{ lanAddr }}</div>
        <div class="row" style="margin-top:8px">
          <button class="sm" @click="copy(lanAddr, 'lan')">{{ copied === 'lan' ? '已复制' : '复制' }}</button>
        </div>
      </div>
      <div class="stat">
        <div class="k">内网使用提示</div>
        <div class="v" style="font-size:12px; font-weight:400; line-height:1.7; color:#56637a">
          手机 / 电脑把 HTTP 或 SOCKS5 代理设为左侧地址即可；端口可在「设置」中修改，国内网站默认直连、境外流量走所选节点。
        </div>
      </div>
    </div>
  </div>

  <div class="card">
    <div class="row between">
      <h2 style="margin:0">当前节点</h2>
      <div class="row">
        <span class="badge" :class="delayClass(nodeDelay)">{{ fmtDelay(nodeDelay) }}</span>
        <button :disabled="testing || !status.running || !currentNode" @click="testCurrent">
          {{ testing ? '测速中…' : '测速' }}
        </button>
        <button @click="emit('goto', 'nodes')">切换节点</button>
      </div>
    </div>
    <div class="stat-grid" style="margin-top:8px">
      <div class="stat"><div class="k">出口链路</div><div class="v mono">{{ chainText }}</div></div>
      <div class="stat"><div class="k">当前节点</div><div class="v mono">{{ currentNode || '—' }}</div></div>
      <div class="stat"><div class="k">延迟</div><div class="v">{{ status.running ? fmtDelay(nodeDelay) : '内核未运行' }}</div></div>
    </div>
    <div class="muted" style="margin-top:8px">
      境外流量经由该节点，国内网站默认直连；延迟为最近一次测速结果，可点「测速」重新测量。
    </div>
  </div>

  <div class="card">
    <div class="row between">
      <h2 style="margin:0">网络测试 <small>经当前生效节点访问下列站点</small></h2>
      <button :disabled="!!testingSite || !status.running || !currentNode" @click="testSites">全部测试</button>
    </div>
    <div class="stat-grid" style="margin-top:8px">
      <div v-for="site in siteList" :key="site.id" class="stat">
        <div class="k">{{ site.label }}</div>
        <div class="v"><span class="badge" :class="delayClass(siteResult[site.id])">{{ fmtDelay(siteResult[site.id]) }}</span></div>
        <div class="row" style="margin-top:8px">
          <button class="sm" :disabled="!!testingSite || !status.running || !currentNode" @click="testSite(site)">
            {{ testingSite === site.id ? '测试中…' : '测试' }}
          </button>
        </div>
      </div>
    </div>
    <div class="muted" style="margin-top:8px">
      测试方式是让「当前生效节点」分别去访问上述站点并返回真实往返延迟；显示「超时」表示该节点访问该站点不通或过慢。
    </div>
  </div>

  <div class="card">
    <h2>运行概况</h2>
    <div class="stat-grid">
      <div class="stat"><div class="k">内核版本</div><div class="v mono">{{ status.mihomo_version || '未运行' }}</div></div>
      <div class="stat"><div class="k">应用版本</div><div class="v mono">{{ status.app_version }}</div></div>
      <div class="stat"><div class="k">订阅数量</div><div class="v">{{ status.sub_count }}</div></div>
      <div class="stat"><div class="k">节点合计</div><div class="v">{{ status.node_count }}</div></div>
      <div class="stat"><div class="k">策略组</div><div class="v">{{ status.group_count }}</div></div>
      <div class="stat"><div class="k">自动更新</div><div class="v">{{ status.auto_update ? ('每 ' + status.auto_hours + ' 小时') : '未开启' }}</div></div>
      <div class="stat"><div class="k">最近更新</div><div class="v">{{ fmtTime(status.last_update) }}</div></div>
    </div>
    <div class="row" style="margin-top:12px">
      <button @click="emit('goto', 'subs')">管理订阅</button>
      <button @click="emit('goto', 'nodes')">查看节点</button>
      <button @click="emit('goto', 'logs')">查看日志</button>
    </div>
  </div>
</template>
