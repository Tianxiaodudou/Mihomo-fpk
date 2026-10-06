# 开发与构建说明

面向需要改代码 / 自行构建的人；面向用户的功能说明请看仓库根目录的 [README.md](../README.md)。

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

## 分流策略（规则优先级）

最终写进内核的 `rules` 顺序固定如下（由 `src/merge.go` 的 `SanitizeRules` 组装，`src/rules_test.go` 锁定）：

| # | 规则 | 去向 | 说明 |
| --- | --- | --- | --- |
| 1 | `IP-CIDR` 保留地址 + `RULE-SET,lancidr` + `RULE-SET,private` | DIRECT | 内网/NAS/路由器直连，永远最前 |
| 2 | `RULE-SET,gfw` | 订阅主策略组 | 被墙域名强制走代理（含 CDN 落在国内 IP 的站点），**先于直连表匹配** |
| 3 | `RULE-SET,direct` + `RULE-SET,cncidr` | DIRECT | 国内域名 / 国内 IP 直连（苹果、微软等国内可直连的域名保持直连） |
| 4 | 订阅自带规则（清洗后） | 按订阅 | 放在内置判定之后，避免订阅里过宽/过时的规则破坏国内直连 |
| 5 | `MATCH,<订阅主策略组>` | 订阅主策略组 | 剩余流量（主要是境外）走节点 |

订阅里依赖地理数据的 `GEOIP,CN` / `GEOSITE,cn` 等条目会被丢弃，由内置 `direct` + `cncidr` 等价顶替
（内核未内嵌 geodata，离线时无法解析这类规则）。

## 规则版本与手动更新

设置页「版本信息」中会显示**规则版本**及其来源：

| 显示 | 含义 |
| --- | --- |
| `日期`（随应用内置） | 使用二进制内置的规则快照，日期 = 该版本的构建日期 |
| `日期`（已手动更新） | 用户在设置页手动拉取过上游最新规则，日期 = 实际更新日期 |

设置页「分流规则」卡片列出各规则集条数与最近更新时间，并提供 **更新分流规则** 按钮：
从上游 `Loyalsoldier/clash-rules` 的 release 分支下载 gfw / direct / cncidr / lancidr / private 五个规则集，
写入 `<var>/rules/`，随后自动重建配置并重载内核，**无需升级应用**（升级应用也不会覆盖手动更新过的规则）。

下载会按顺序尝试多个镜像（jsdelivr → ghfast.top → ghproxy.net → raw.githubusercontent），
每个镜像失败后会重试一轮，直连全部失败时再走本机代理端口重试；五个规则集并行拉取以缩短等待。
任一规则集拉取失败则整体不落盘（提示里会列出各镜像的失败原因），保证原有规则始终可用。

对应接口：`GET /api/rules`（当前规则版本与条数）、`POST /api/rules/update`（手动更新）。

## 本地构建

依赖：Python 3.8+、Go 1.22+、Node.js 18+。

```bash
python tools/build.py            # 全流程：web -> rules -> go -> mihomo -> icons -> stage -> pack
python tools/build.py web go     # 只跑指定步骤
python tools/build.py rules      # 只更新内置分流规则
```

`rules` 步骤会从 [Loyalsoldier/clash-rules](https://github.com/Loyalsoldier/clash-rules)（`release` 分支）
拉取最新 `direct/private/lancidr/cncidr/gfw` 到 `src/rules/`，由 `go:embed` 打进二进制；
**拉取失败不中断构建**，沿用仓内快照，保证离线可构建、安装后离线可用。

Go 单元测试（需 Linux，源码依赖 Linux syscall；Windows 下请用 `GOOS=linux` 交叉编译）：

```bash
cd src && go test ./... -count=1
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

## 版本号如何递增

`.github/workflows/build.yml` 在每次流程运行时：

1. `tools/bump_version.py` 把 `manifest` 的版本号 patch +1（如 `1.0.25` → `1.0.26`）；
2. 构建前端与双架构后端、下载内核、按架构分别组装 stage、为每个架构各打一个 `.fpk`（`<版本>-<架构>-MihomoProxy.fpk`）；
3. 把版本号提交回 `main`（提交信息带 `[skip ci]`，避免再次触发构建）；
4. 用该版本号创建 tag `vX.Y.Z` 并发布 Release，附带 `.fpk` 与 `SHA256SUMS.txt`。

向 `main` 推送任何代码即会自动出一个新版本；也可以在 Actions 页手动 `workflow_dispatch` 触发。

## 打包说明

`tools/pack_fpk.py` 是自研的 `.fpk` 打包器（不依赖官方 `fnpack`），产物结构与官方一致，
细节见 [fpk-format.md](fpk-format.md)。
