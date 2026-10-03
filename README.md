# 显式代理 MihomoProxy（fnOS 应用）

基于 [Mihomo](https://github.com/MetaCubeX/mihomo) 内核的飞牛 fnOS 代理应用：

* 单文件安装包（`.fpk`），仅需一个网关端口即可使用；
* Web 控制台：订阅管理、节点/策略组切换、测速、延迟徽标、配置保存；
* Go 后端把 mihomo 内核（RESTful API + 工作目录）托管为 fnOS 应用的网关服务；
* 内置直连/私有/局域网/CN CIDR 规则集（编译进二进制，无外网依赖）。

## 目录结构

| 路径 | 说明 |
| --- | --- |
| `manifest` | fnOS 应用元信息（名称、版本、平台…）。**版本号由 CI 自动递增** |
| `cmd/` | 应用生命周期脚本（install/upgrade/uninstall/config 的 init/callback，由 fnOS 调用） |
| `config/` | `privilege`（提权/隔离策略）、`resource`（资源声明） |
| `app/` | 安装到应用目录的内容：`bin/`（内核+后端）、`etc/`（配置模板）、`ui/`（图标与 UI 配置） |
| `src/` | Go 后端源码（`go:embed` 打进二进制） |
| `web/` | Vue 3 + Vite 前端源码，构建产物输出到 `src/static/` |
| `etc/` | `config.yaml.template` 模板源文件 |
| `tools/` | 构建与打包脚本（纯 Python 标准库） |

## 本地构建

依赖：Python 3.8+、Go 1.22+、Node.js 18+。

```bash
python tools/build.py            # 全流程：web -> go -> mihomo -> icons -> stage -> pack
python tools/build.py web go     # 只跑指定步骤
```

产物：`build/MihomoProxy_<版本>.fpk`。

可覆盖的环境变量：

| 变量 | 默认值 | 说明 |
| --- | --- | --- |
| `MIHOMO_VERSION` | `v1.19.32` | 内核版本（从 GitHub Releases 下载） |
| `MIHOMO_GZ_X86_64` / `MIHOMO_GZ_AARCH64` | 空 | 本地已有的内核包路径，填了就不下载 |
| `NPM_REGISTRY` | `https://registry.npmmirror.com` | 前端依赖源 |
| `GOPROXY` | `https://goproxy.cn,direct` | Go 模块代理 |
| `SOURCE_DATE_EPOCH` | 仓库最近提交时间 | 打包时写进 tar 头的时间戳（可复现构建） |

安装：飞牛 fnOS → 应用中心 → 手动安装 → 选择 `.fpk`。

## 版本号如何递增

`.github/workflows/build.yml` 在每次流程运行时：

1. `tools/bump_version.py` 把 `manifest` 的版本号 patch +1（如 `1.0.14` → `1.0.15`）；
2. 构建前端与双架构后端、下载内核、组装 stage、打包 `.fpk`；
3. 把版本号提交回 `main`（提交信息带 `[skip ci]`，避免再次触发构建）；
4. 用该版本号创建 tag `vX.Y.Z` 并发布 Release，附带 `.fpk` 与 `SHA256SUMS.txt`。

向 `main` 推送任何代码即会自动出一个新版本；也可以在 Actions 页手动 `workflow_dispatch` 触发。

## 打包说明

`tools/pack_fpk.py` 是自研的 `.fpk` 打包器（不依赖官方 `fnpack`），产物结构与官方一致，
细节见 [docs/fpk-format.md](docs/fpk-format.md)。

## 许可

仅供个人在自有设备上使用；Mihomo 内核遵循其上游许可（GPL-3.0）。
