#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""fnOS 应用打包器 (.fpk) —— 复刻官方 fnpack 的输出结构，纯标准库、无外部依赖。

产物结构（已与 fnpack 生成的 .fpk 逐成员比对一致）::

    .fpk = gzip(tar)                     # tar: USTAR; gzip: mtime=0, OS=255
        +-- app.tgz                      # gzip(tar): app/** 与 config/**（安装到应用目录）
        +-- cmd/**                       # 生命周期脚本（强制 LF）
        +-- config/**                    # privilege / resource
        +-- ICON.PNG
        +-- ICON_256.PNG
        +-- manifest                     # 自动追加 checksum = md5(app.tgz)

打包时对两个字段做与 fnpack 一致的改写：
  * manifest 统一以 CRLF 写入（保留键值顺序与对齐空格）；
  * `platform = a;b;c` 这种多值写法会被拆开，除第一个以外的值写成 `; b` 注释行；
  * 末尾追加 `checksum = <md5(app.tgz)>`。

用法::

    python tools/pack_fpk.py <stagedir> [-o out.fpk]

stagedir 需包含: manifest / ICON.PNG / ICON_256.PNG / app/ / cmd/ / config/ [/ wizard/]
"""
import gzip
import hashlib
import io
import os
import re
import sys
import tarfile

MODE_DIR = 0o777
MODE_FILE = 0o666
DEFAULT_MTIME = 1700000000


def log(msg):
    print("[pack] " + msg, flush=True)


def _entries(root):
    """按字节序遍历：目录项先于其内容，文件与目录混排。"""
    for name in sorted(os.listdir(root), key=lambda s: s.encode("utf-8")):
        full = os.path.join(root, name)
        if os.path.isdir(full):
            yield full, True
        else:
            yield full, False


def _walk(root, prefix=""):
    """产出 (归档内相对路径, 磁盘路径, 是否目录)。"""
    for full, isdir in _entries(root):
        rel = prefix + os.path.basename(full)
        if isdir:
            yield rel, full, True
            for item in _walk(full, rel + "/"):
                yield item
        else:
            yield rel, full, False


def _tarinfo(name, mode, size, mtime, isdir):
    ti = _DirInfo(name) if isdir else tarfile.TarInfo(name)
    ti.type = tarfile.DIRTYPE if isdir else tarfile.REGTYPE
    ti.mode = mode
    ti.uid = ti.gid = 0
    ti.uname = ti.gname = ""
    ti.mtime = mtime
    ti.size = 0 if isdir else size
    return ti


class _DirInfo(tarfile.TarInfo):
    """fnpack(Go) 写目录项时不带结尾斜杠，这里保持一致。"""

    def get_info(self):
        info = tarfile.TarInfo.get_info(self)
        name = info.get("name")
        if self.isdir() and name and name.endswith("/"):
            info["name"] = name[:-1]
        return info


def _fix_headers(data):
    """把 tarfile 写的头修补成 fnpack(Go) 的字节形态。

    Python 对非设备文件把 devmajor/devminor 写成整段 NUL，Go 写 "0000000\\0"，
    因此需要回填这两个字段并重算 POSIX 校验和。
    """
    buf = bytearray(data)
    i = 0
    while (i + 1) * 512 <= len(buf):
        off = i * 512
        head = bytes(buf[off:off + 512])
        if head == b"\x00" * 512:
            break
        size = int(head[124:136].split(b"\x00")[0].strip() or b"0", 8)
        if head[329:337] != b"0000000\x00" or head[337:345] != b"0000000\x00":
            buf[off + 329:off + 337] = b"0000000\x00"
            buf[off + 337:off + 345] = b"0000000\x00"
        buf[off + 148:off + 156] = b" " * 8
        chksum = sum(buf[off:off + 512])
        buf[off + 148:off + 156] = ("%06o" % chksum).encode() + b"\x00 "
        i += 1 + (size + 511) // 512
    return bytes(buf)


def _gzip_tar_add(out_path, add_members, mtime):
    """写出 gzip(tar)；gzip 头与 fnpack 一致（FLG=0 / MTIME=0 / XFL=0 / OS=255）。

    tarfile 默认会把归档补齐到 10240 字节记录边界，fnpack 只写两个结束零块，
    这里截掉多余填充；另外回填 devmajor/devminor 以保持逐字节一致。
    """
    buf = io.BytesIO()
    with tarfile.open(fileobj=buf, mode="w", format=tarfile.USTAR_FORMAT) as tar:
        add_members(tar)
        end = buf.tell()
    data = _fix_headers(buf.getvalue()[:end + 1024])
    del buf
    with open(out_path, "wb") as raw:
        with gzip.GzipFile(filename="", mode="wb", fileobj=raw,
                           compresslevel=6, mtime=0) as gz:
            gz.write(data)
    return out_path


def build_app_tgz(stage, out_path, mtime):
    """把 app/ 与 config/ 的内容（去掉这一层前缀）打进 app.tgz。"""
    def add(tar):
        # app/ 的内容不带前缀直接铺到根部
        d = os.path.join(stage, "app")
        if os.path.isdir(d):
            for rel, full, isdir in _walk(d):
                if isdir:
                    tar.addfile(_tarinfo(rel, MODE_DIR, 0, mtime, True))
                else:
                    with open(full, "rb") as f:
                        data = f.read()
                    tar.addfile(_tarinfo(rel, MODE_FILE, len(data), mtime, False),
                                io.BytesIO(data))
        # config/（以及可选的 wizard/）以自身目录名作为前缀一并打进 app.tgz
        for sub in ("config", "wizard"):
            d = os.path.join(stage, sub)
            if not os.path.isdir(d):
                continue
            tar.addfile(_tarinfo(sub, MODE_DIR, 0, mtime, True))
            for rel, full, isdir in _walk(d, sub + "/"):
                if isdir:
                    tar.addfile(_tarinfo(rel, MODE_DIR, 0, mtime, True))
                else:
                    with open(full, "rb") as f:
                        data = f.read()
                    tar.addfile(_tarinfo(rel, MODE_FILE, len(data), mtime, False),
                                io.BytesIO(data))
    _gzip_tar_add(out_path, add, mtime)
    log("app.tgz -> %s (%.1f MB)" % (out_path, os.path.getsize(out_path) / 1048576.0))
    return out_path


def rewrite_manifest(src_bytes, app_md5):
    """复刻 fnpack 对 manifest 的改写，返回写入 fpk 的字节（CRLF）。"""
    text = src_bytes.replace(b"\r\n", b"\n").decode("utf-8").rstrip("\n")
    out = []
    for line in text.split("\n"):
        m = re.match(r"^(\s*platform\s*=\s*)(.*?)\s*$", line)
        if m:
            parts = [p.strip() for p in m.group(2).split(";") if p.strip()]
            for extra in parts[1:]:
                out.append("; " + extra)
            if parts:
                out.append(m.group(1) + parts[0])
                continue
        out.append(line)
    out.append("checksum              = " + app_md5)
    return ("\r\n".join(out) + "\r\n").encode("utf-8")


def _check_line_endings(stage):
    """cmd/ 下的脚本在 fnOS 上由 bash 执行，必须是 LF（CRLF 会让 bash 直接报错）。"""
    cmd = os.path.join(stage, "cmd")
    bad = []
    for name in sorted(os.listdir(cmd)):
        p = os.path.join(cmd, name)
        if os.path.isfile(p) and b"\r\n" in open(p, "rb").read():
            bad.append(name)
    if bad:
        raise SystemExit("cmd/ 下存在 CRLF 文件（会破坏 fnOS 上的 bash 执行）: " + ", ".join(bad))


def pack(stage, out_path, mtime=None):
    stage = os.path.abspath(stage)
    for req in ("manifest", "cmd", "config"):
        if not os.path.exists(os.path.join(stage, req)):
            raise SystemExit("缺少必需条目: " + req)
    _check_line_endings(stage)
    if mtime is None:
        mtime = int(os.environ.get("SOURCE_DATE_EPOCH") or 0) or DEFAULT_MTIME

    tmp_app = os.path.join(os.path.dirname(os.path.abspath(out_path)), "_app.tgz")
    build_app_tgz(stage, tmp_app, mtime)
    with open(tmp_app, "rb") as f:
        app_md5 = hashlib.md5(f.read()).hexdigest()
    log("checksum (md5 app.tgz) = " + app_md5)

    with open(os.path.join(stage, "manifest"), "rb") as f:
        manifest = rewrite_manifest(f.read(), app_md5)

    def add(tar):
        with open(tmp_app, "rb") as f:
            data = f.read()
        tar.addfile(_tarinfo("app.tgz", MODE_FILE, len(data), mtime, False), io.BytesIO(data))
        for sub in ("cmd", "config"):
            d = os.path.join(stage, sub)
            if not os.path.isdir(d):
                continue
            tar.addfile(_tarinfo(sub, MODE_DIR, 0, mtime, True))
            for rel, full, isdir in _walk(d, sub + "/"):
                if isdir:
                    tar.addfile(_tarinfo(rel, MODE_DIR, 0, mtime, True))
                else:
                    with open(full, "rb") as f:
                        data = f.read()
                    if sub == "cmd":
                        data = data.replace(b"\r\n", b"\n")
                    tar.addfile(_tarinfo(rel, MODE_FILE, len(data), mtime, False),
                                io.BytesIO(data))
        for extra in ("ICON.PNG", "ICON_256.PNG"):
            p = os.path.join(stage, extra)
            if os.path.isfile(p):
                with open(p, "rb") as f:
                    data = f.read()
                tar.addfile(_tarinfo(extra, MODE_FILE, len(data), mtime, False),
                            io.BytesIO(data))
        tar.addfile(_tarinfo("manifest", MODE_FILE, len(manifest), mtime, False),
                    io.BytesIO(manifest))

    _gzip_tar_add(out_path, add, mtime)
    os.remove(tmp_app)
    log("FPK -> %s (%.1f MB)" % (out_path, os.path.getsize(out_path) / 1048576.0))
    return out_path


def main(argv):
    if len(argv) < 2:
        raise SystemExit(__doc__)
    stage = argv[1]
    out = None
    if "-o" in argv:
        out = argv[argv.index("-o") + 1]
    if not out:
        ver = "unknown"
        with open(os.path.join(stage, "manifest"), encoding="utf-8", errors="ignore") as f:
            for line in f:
                if re.match(r"^\s*version\s*=", line):
                    ver = line.split("=", 1)[1].strip()
        out = os.path.join(os.getcwd(), "MihomoProxy_%s.fpk" % ver)
    pack(stage, out)


if __name__ == "__main__":
    main(sys.argv)
