# fpk 包格式（实测逆向）

`.fpk` 是 tar.gz，但内部的 tar 头部细节（格式、权限、时间戳、成员顺序）都被 fnOS 校验，
本仓库的 `tools/pack_fpk.py` 按下面的规则实现，产物与官方 `fnpack` 的包逐成员一致
（`app.tgz` 解压后的 tar 载荷可做到逐字节相同）。

## 外层

```
.fpk = gzip(tar)  # gzip: FLG=0, MTIME=0, XFL=0, OS=255; tar: USTAR
├── app.tgz       # 安装到应用目录的内容（自身也是 gzip(tar)）
├── cmd/          # 生命周期脚本
├── config/       # privilege / resource
├── ICON.PNG
├── ICON_256.PNG
└── manifest
```

规则：

* tar 用 **USTAR**（magic `ustar\0` + version `00`），非 GNU 格式；
* 成员顺序固定为上面列出的顺序，`cmd` 与 `config` **目录项本身也要写进 tar**；
* 目录项权限 `0777`、文件 `0666`；`uid=gid=0`，`uname`/`gname` 为空；
* 目录名 **不带结尾斜杠**（`bin`，不是 `bin/`）；
* 头里 `devmajor`/`devminor` 写 `0000000\0`（不是全 NUL）；
* tar 只写两个结束零块，**不做 10240 字节记录对齐**；
* gzip 头固定 FLG=0、MTIME=0、XFL=0、OS=255。

Python `tarfile` 的默认行为与之有三处差异，需逐一修补（见 `_fix_headers` / `_DirInfo`）：
目录名会自动加 `/`、结束时补齐到 10240 字节、`devmajor/devminor` 写全 NUL。

## app.tgz

`app.tgz` 内的路径**不带 `app/` 前缀**：

```
bin/<架构>/{MihomoProxy-web,mihomo}   # 单架构包只含本架构目录（manifest.platform 也只声明本架构）
etc/config.yaml.template
ui/config, ui/images/icon_{64,256}.png
config/          # 注意：config/ 也会被打进 app.tgz
```

`app.tgz` 用与外层相同的 tar/gzip 规则。

## manifest 的改写

`fnpack` 打包时会重写 `manifest`（源码里保持原本行尾，写出时统一 CRLF）：

1. 所有行尾统一为 `\r\n`；
2. `platform = x86;arm` 这种分号列表会拆成两行：`; arm` 与 `platform = x86`
   （保留字段名后的对齐空格）；
3. 末尾追加一行 `checksum              = <md5(app.tgz)>`；
4. 文件以 `\r\n` 结束。

校验值就是 `app.tgz` 这个文件本身的 md5（压缩后的字节），所以打包顺序必须是
**先生成 app.tgz → 再算 md5 → 再改写 manifest**。

## 行尾与二进制

* `cmd/` 下的脚本必须是 LF，否则 fnOS 无法执行；源码仓库里允许是 CRLF，
  由 `tools/build.py` 的 `stage()` 在组装时统一转换。
* 二进制（ELF 内核与后端）必须按字节原样拷贝，绝不能在组装时做 CRLF→LF 替换，
  否则 ELF 里恰好出现的 `0x0D 0x0A` 会被删掉，导致内核启动即崩。
* 仓库里用 `.gitattributes` 的 `* -text` 关闭行尾自动转换，保证字节稳定。
