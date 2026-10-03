#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""MihomoProxy .fpk 构建脚本（本地 / GitHub Actions 通用，仅依赖 Python 标准库）

用法：
    python tools/build.py            # 全流程：web -> go -> mihomo -> icons -> stage -> pack
    python tools/build.py web go     # 只跑指定步骤

可用环境变量覆盖：
    MIHOMO_VERSION   内核版本（默认 v1.19.32）
    MIHOMO_GZ_X86_64 / MIHOMO_GZ_AARCH64  本地已有的 mihomo 发行包(.gz)，跳过下载
    NPM_REGISTRY     默认 https://registry.npmmirror.com
    GOPROXY          默认 https://goproxy.cn,direct
    SOURCE_DATE_EPOCH  打进 tar 头的时间戳（默认取仓库最近一次提交时间）
"""
import argparse
import gzip
import os
import shutil
import subprocess
import sys
import urllib.request

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
BUILD = os.path.join(ROOT, "build")
PKG = os.path.join(BUILD, "pkg")
WEB = os.path.join(ROOT, "web")
SRC = os.path.join(ROOT, "src")
STATIC = os.path.join(SRC, "static")
TOOLS = os.path.join(ROOT, "tools")

MIHOMO_VERSION = os.environ.get("MIHOMO_VERSION", "v1.19.32")
MIHOMO_URL = "https://github.com/MetaCubeX/mihomo/releases/download/{v}/mihomo-linux-{a}-{v}.gz"
NPM_REGISTRY = os.environ.get("NPM_REGISTRY", "https://registry.npmmirror.com")
GOPROXY = os.environ.get("GOPROXY", "https://goproxy.cn,direct")
ARCHS = (("amd64", "x86_64"), ("arm64", "aarch64"))

sys.path.insert(0, TOOLS)


def log(msg):
    print("[build] " + str(msg), flush=True)


def _exe(name):
    for cand in (name, name + ".cmd", name + ".exe"):
        p = shutil.which(cand)
        if p:
            return p
    raise SystemExit("未找到可执行命令: " + name)


def run(args, cwd=None, env=None):
    log("$ " + " ".join(args))
    r = subprocess.run(args, cwd=cwd, env=env)
    if r.returncode != 0:
        raise SystemExit("命令失败(%d): %s" % (r.returncode, " ".join(args)))
    return r


def manifest_version():
    with open(os.path.join(ROOT, "manifest"), encoding="utf-8") as f:
        for line in f:
            if line.strip().startswith("version"):
                return line.split("=", 1)[1].strip()
    return "0.0.0"


# --------------------------------------------------------------------------
# 1. 前端（vite -> src/static，供 Go embed）
# --------------------------------------------------------------------------
def build_web():
    log("构建前端 ...")
    npm = _exe("npm")
    if not os.path.isdir(os.path.join(WEB, "node_modules")):
        sub = "ci" if os.path.isfile(os.path.join(WEB, "package-lock.json")) else "install"
        run([npm, sub, "--registry=" + NPM_REGISTRY, "--no-audit", "--no-fund"], cwd=WEB)
    run([npm, "run", "build"], cwd=WEB)
    if not os.path.isfile(os.path.join(STATIC, "index.html")):
        raise SystemExit("前端产物缺失: " + STATIC)


# --------------------------------------------------------------------------
# 2. Go 后端（linux/amd64 + linux/arm64，静态链接）
# --------------------------------------------------------------------------
def build_go():
    log("构建 Go 后端 ...")
    go = _exe("go")
    for arch, dirname in ARCHS:
        outdir = os.path.join(ROOT, "app", "bin", dirname)
        os.makedirs(outdir, exist_ok=True)
        env = dict(os.environ)
        env.update({"GOOS": "linux", "GOARCH": arch, "CGO_ENABLED": "0", "GOPROXY": GOPROXY})
        env.setdefault("GOTOOLCHAIN", "local")
        run([go, "build", "-trimpath", "-ldflags", "-s -w",
             "-o", os.path.join(outdir, "MihomoProxy-web"), "."], cwd=SRC, env=env)
        log("  linux/%s -> app/bin/%s/MihomoProxy-web" % (arch, dirname))


# --------------------------------------------------------------------------
# 3. mihomo 内核（下载官方发布包并解压）
# --------------------------------------------------------------------------
def build_mihomo():
    log("准备 mihomo 内核 (%s) ..." % MIHOMO_VERSION)
    for arch, dirname in ARCHS:
        outdir = os.path.join(ROOT, "app", "bin", dirname)
        os.makedirs(outdir, exist_ok=True)
        dst = os.path.join(outdir, "mihomo")
        local = os.environ.get("MIHOMO_GZ_" + dirname.upper())
        if local and os.path.isfile(local):
            with gzip.open(local, "rb") as f, open(dst, "wb") as o:
                shutil.copyfileobj(f, o)
        else:
            url = MIHOMO_URL.format(v=MIHOMO_VERSION, a=arch)
            log("  GET " + url)
            req = urllib.request.Request(url, headers={"User-Agent": "MihomoProxy-build"})
            with urllib.request.urlopen(req, timeout=600) as resp, \
                    gzip.open(resp, "rb") as f, open(dst, "wb") as o:
                shutil.copyfileobj(f, o)
        os.chmod(dst, 0o755)
        log("  -> %s (%.1f MB)" % (dst, os.path.getsize(dst) / 1048576.0))


# --------------------------------------------------------------------------
# 4. 图标（仓库内已生成，同步到包根目录）
# --------------------------------------------------------------------------
def build_icons():
    log("同步图标 ...")
    imgdir = os.path.join(ROOT, "app", "ui", "images")
    for src, dst in ((os.path.join(imgdir, "icon_64.png"), os.path.join(ROOT, "ICON.PNG")),
                     (os.path.join(imgdir, "icon_256.png"), os.path.join(ROOT, "ICON_256.PNG"))):
        if os.path.isfile(src):
            shutil.copyfile(src, dst)
            log("  %s (%d bytes)" % (os.path.basename(dst), os.path.getsize(dst)))


# --------------------------------------------------------------------------
# 5. 组装 stage 目录（只放运行期需要的文件；文本统一 LF，二进制原样）
# --------------------------------------------------------------------------
def _copy_file(src, dst, mode=0o644, text=True):
    """text=True 会把 CRLF 统一成 LF（shell 脚本必须 LF，否则 fnOS 无法执行）；
    text=False 为字节级原样拷贝，绝不能替换——ELF 里恰好出现的 0x0D0A 会被破坏。"""
    os.makedirs(os.path.dirname(dst), exist_ok=True)
    with open(src, "rb") as f:
        data = f.read()
    if text:
        data = data.replace(b"\r\n", b"\n")
    with open(dst, "wb") as f:
        f.write(data)
    os.chmod(dst, mode)


def stage():
    log("组装包目录 -> " + PKG)
    if os.path.isdir(PKG):
        shutil.rmtree(PKG)
    os.makedirs(PKG)

    _copy_file(os.path.join(ROOT, "manifest"), os.path.join(PKG, "manifest"))
    for extra in ("ICON.PNG", "ICON_256.PNG"):
        p = os.path.join(ROOT, extra)
        if os.path.isfile(p):
            _copy_file(p, os.path.join(PKG, extra), text=False)
    for name in ("privilege", "resource"):
        _copy_file(os.path.join(ROOT, "config", name), os.path.join(PKG, "config", name))
    # app/ 的内容由 fnOS 安装到 $TRIM_APPDEST，模板须放 app/etc/ 下
    _copy_file(os.path.join(ROOT, "etc", "config.yaml.template"),
               os.path.join(PKG, "app", "etc", "config.yaml.template"))
    cmd_src = os.path.join(ROOT, "cmd")
    for name in sorted(os.listdir(cmd_src)):
        src = os.path.join(cmd_src, name)
        if os.path.isfile(src):
            _copy_file(src, os.path.join(PKG, "cmd", name), 0o755)
    _copy_file(os.path.join(ROOT, "app", "ui", "config"), os.path.join(PKG, "app", "ui", "config"))
    for name in ("icon_64.png", "icon_256.png"):
        p = os.path.join(ROOT, "app", "ui", "images", name)
        if os.path.isfile(p):
            _copy_file(p, os.path.join(PKG, "app", "ui", "images", name), text=False)
    for dirname in ("x86_64", "aarch64"):
        for name in ("MihomoProxy-web", "mihomo"):
            src = os.path.join(ROOT, "app", "bin", dirname, name)
            if not os.path.isfile(src):
                raise SystemExit("缺少二进制: " + src)
            _copy_file(src, os.path.join(PKG, "app", "bin", dirname, name), 0o755, text=False)
    log("组装完成: " + PKG)


# --------------------------------------------------------------------------
# 6. 打包 .fpk（自研打包器，产物结构与官方 fnpack 一致）
# --------------------------------------------------------------------------
def pack():
    import pack_fpk
    ver = manifest_version()
    out = os.path.join(BUILD, "MihomoProxy_%s.fpk" % ver)
    log("打包 .fpk ...")
    pack_fpk.pack(PKG, out)
    log("FPK: %s (%.1f MB)" % (out, os.path.getsize(out) / 1048576.0))
    return out


STEPS = [("web", build_web), ("go", build_go), ("mihomo", build_mihomo),
         ("icons", build_icons), ("stage", stage), ("pack", pack)]


def main():
    ap = argparse.ArgumentParser(description="MihomoProxy fpk 构建")
    ap.add_argument("steps", nargs="*", help="要执行的步骤（默认全部）")
    args = ap.parse_args()
    names = args.steps or [n for n, _ in STEPS]
    table = dict(STEPS)
    for n in names:
        if n not in table:
            raise SystemExit("未知步骤: %s（可选 %s）" % (n, "/".join(table)))
    for n in names:
        table[n]()


if __name__ == "__main__":
    main()
