// 策略组 / 节点 通用解析工具（供节点页与概览页共用）
// 注意：所有对外函数的第一个参数都接受「内核原始响应 {proxies:{...}}」或「纯 proxies 映射」两种形式。

export function proxyMap(raw) {
  if (!raw) return {}
  const m = raw.proxies
  return m && typeof m === 'object' ? m : raw
}

export function isGroup(p) {
  return !!(p && Array.isArray(p.all) && p.all.length && /Selector|URLTest|Fallback|LoadBalance|Relay/i.test(p.type || ''))
}

export function listGroups(proxies) {
  return Object.values(proxyMap(proxies) || {}).filter(isGroup)
}

// 规则出口组：订阅自带名为 PROXY 的组则用它，否则用订阅里第一个可手动选择的组（排除内核内置 GLOBAL）。
// 这与后端 ExitGroupName() 的判定保持一致：界面上的组全部来自订阅，不存在应用自造的“幽灵组”。
export function primaryGroup(proxies) {
  const all = listGroups(proxies)
  const gs = all.filter((g) => g.name !== 'GLOBAL')
  if (!gs.length) return all[0] || null
  return gs.find((g) => g.name === 'PROXY') || gs.find((g) => /Selector|URLTest|Fallback/i.test(g.type || '')) || gs[0]
}

// 沿 now 字段展开链路：PROXY → 自动选择 → 🇯🇵日本 05
export function resolveChain(proxies, start) {
  const m = proxyMap(proxies) || {}
  const chain = []
  let cur = start
  const seen = new Set()
  while (cur && !seen.has(cur)) {
    seen.add(cur)
    chain.push(cur)
    const p = m[cur]
    if (!p || !p.now) break
    cur = p.now
  }
  return chain
}

// 链路上最后一个真实节点（非策略组）；若链路只有策略组，返回末端
export function effectiveNode(proxies, chain) {
  const arr = Array.isArray(chain) ? chain : resolveChain(proxies, chain)
  const m = proxyMap(proxies) || {}
  for (let i = arr.length - 1; i >= 0; i--) {
    const p = m[arr[i]]
    if (p && !isGroup(p)) return arr[i]
  }
  return arr.length ? arr[arr.length - 1] : ''
}

// 内核缓存的最近一次测速结果（未主动测速时也能显示）
export function historyDelay(proxies, name) {
  const m = proxyMap(proxies) || {}
  const p = m[name]
  const h = (p && p.history) || []
  for (let i = h.length - 1; i >= 0; i--) {
    const d = h[i] && h[i].delay
    if (typeof d === 'number' && d > 0) return d
  }
  return null
}

export function fmtDelay(d) {
  if (d === undefined || d === null) return '未测速'
  if (typeof d !== 'number' || d < 0) return '超时'
  return d + ' ms'
}

export function delayClass(d) {
  if (d === undefined || d === null) return ''
  if (typeof d !== 'number' || d < 0) return 'err'
  if (d < 300) return 'fast'
  return 'warn'
}
