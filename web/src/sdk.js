// 飞牛宿主 SDK 封装：优先使用 @trimjs/web-app，失败时回退到浏览器原生选择器。
import TrimApp from '@trimjs/web-app'

let trim = null
try { trim = new TrimApp() } catch (e) { trim = null }

export async function setTitle(title) {
  try { if (trim) await trim.setTitle(title) } catch (e) {}
}

// 宿主内只用飞牛自带的文件选择器（NAS 上的文件）：
//   - 选中 → { path }（NAS 绝对路径，交给后端读取）
//   - 取消 → null（**不再回退到浏览器原生 input**，避免弹出电脑端的文件对话框）
// 只有在宿主 SDK 不可用（例如浏览器直接打开调试）时，才回退到 input + FileReader。
const HOST_PICKER = !!(trim && (typeof trim.pickUserFile === 'function' || typeof trim.pickFile === 'function'))

export function hasHostPicker() {
  return HOST_PICKER
}

// 从宿主返回体里取出路径：pickUserFile/pickSharedFile 返回 { code, msg, data }，pickFile 直接返回 string[]
function pickPathsFromResponse(res) {
  if (!res) return []
  if (Array.isArray(res)) return res.filter((x) => typeof x === 'string' && x)
  if (typeof res === 'string') return [res]
  const data = res.data !== undefined ? res.data : res
  const list = Array.isArray(data) ? data : (data && Array.isArray(data.paths) ? data.paths : [])
  return list.map((x) => (typeof x === 'string' ? x : (x && (x.path || x.realPath)) || '')).filter(Boolean)
}

// 返回 { path } | { content } | null(用户取消)
// 宿主内优先用 pickUserFile：飞牛官方语义 = 选择文件并把该文件授权给当前应用
// （SDK 注释：pickFile 仅"选择文件"；pickUserFile 为"选择用户文件并授权给当前应用"，
//   对应 Scope trim.file.userAccess。用 pickFile 时不会产生授权，应用进程读不到该文件，
//   用户就得自己去应用中心手动给目录授权。）
export async function pickSubscriptionFile() {
  if (HOST_PICKER) {
    const params = {
      multiple: false,
      accept: ['.yaml', '.yml', '.txt', '.conf', '.json', '.list', '.ini', '.base64'],
      title: '选择订阅文件'
    }
    let res
    if (typeof trim.pickUserFile === 'function') res = await trim.pickUserFile(params)
    else res = await trim.pickFile(params)
    const paths = pickPathsFromResponse(res)
    if (paths.length) return { path: paths[0] }
    return null
  }
  return pickByInput()
}

function pickByInput() {
  return new Promise((resolve, reject) => {
    const input = document.createElement('input')
    input.type = 'file'
    input.accept = '.yaml,.yml,.txt,.conf,.json,.list,.ini,.base64'
    input.style.display = 'none'
    input.onchange = () => {
      const f = input.files && input.files[0]
      if (!f) { reject(new Error('未选择文件')); return }
      const fr = new FileReader()
      fr.onload = () => resolve({ content: String(fr.result || ''), fileName: f.name })
      fr.onerror = () => reject(new Error('读取文件失败'))
      fr.readAsText(f)
    }
    document.body.appendChild(input)
    input.click()
    setTimeout(() => { try { document.body.removeChild(input) } catch (e) {} }, 60000)
  })
}
