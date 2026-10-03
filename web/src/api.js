// 统一网关下的 API 客户端。路径自适应 /app/<appname> 前缀。
const m = location.pathname.match(/^\/app\/[^/]+/)
export const BASE = m ? m[0] : ''

export function wsURL(path) {
  const proto = location.protocol === 'https:' ? 'wss:' : 'ws:'
  return proto + '//' + location.host + BASE + path
}

async function request(method, path, body) {
  const opt = { method, headers: {}, cache: 'no-store' }
  if (body !== undefined) {
    opt.headers['Content-Type'] = 'application/json'
    opt.body = JSON.stringify(body)
  }
  const r = await fetch(BASE + path, opt)
  const text = await r.text()
  let data = null
  try { data = text ? JSON.parse(text) : null } catch (e) { data = { error: text } }
  if (!r.ok) {
    const msg = (data && (data.error || data.message)) || ('请求失败 (' + r.status + ')')
    throw new Error(msg)
  }
  return data
}

export const api = {
  get: (p) => request('GET', p),
  post: (p, b) => request('POST', p, b),
  put: (p, b) => request('PUT', p, b),
  del: (p) => request('DELETE', p),
  // 纯文本响应（如配置预览）
  text: async (path) => {
    const r = await fetch(BASE + path, { cache: 'no-store' })
    return await r.text()
  },
  // 原始响应（用于内核透传接口，如延迟测速，非 2xx 也返回数据）
  raw: async (path) => {
    const r = await fetch(BASE + path, { cache: 'no-store' })
    const text = await r.text()
    let data = null
    try { data = text ? JSON.parse(text) : null } catch (e) { data = { message: text } }
    return { status: r.status, ok: r.ok, data }
  }
}
