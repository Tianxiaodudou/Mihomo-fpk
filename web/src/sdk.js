// 飞牛宿主 SDK 封装：优先使用 @trimjs/web-app，失败时回退到浏览器原生选择器。
import TrimApp from '@trimjs/web-app'

let trim = null
try { trim = new TrimApp() } catch (e) { trim = null }

export async function setTitle(title) {
  try { if (trim) await trim.setTitle(title) } catch (e) {}
}

// 返回 { path } 或 { content }
export async function pickSubscriptionFile() {
  if (trim && typeof trim.pickFile === 'function') {
    try {
      const files = await trim.pickFile({
        multiple: false,
        accept: ['.yaml', '.yml', '.txt', '.conf', '.json', '.list', '.ini', '.base64'],
        title: '选择订阅文件'
      })
      if (files && files.length) return { path: files[0] }
    } catch (e) { /* 宿主不支持时回退 */ }
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
