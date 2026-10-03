#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""递增 manifest 里的版本号（默认 patch +1），并把新版本号打印出来。

用法：
    python tools/bump_version.py            # 1.0.14 -> 1.0.15
    python tools/bump_version.py --minor    # 1.0.14 -> 1.1.0
    python tools/bump_version.py --major    # 1.0.14 -> 2.0.0

在 GitHub Actions 中会把 version=... 追加写入 $GITHUB_OUTPUT。
"""
import os
import re
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
MANIFEST = os.path.join(ROOT, "manifest")


def bump(version, part="patch"):
    nums = [int(x) for x in re.findall(r"\d+", version)[:3]]
    while len(nums) < 3:
        nums.append(0)
    idx = {"major": 0, "minor": 1, "patch": 2}[part]
    nums[idx] += 1
    for i in range(idx + 1, 3):
        nums[i] = 0
    return ".".join(str(x) for x in nums)


def main():
    part = "patch"
    for a in sys.argv[1:]:
        if a.startswith("--") and a[2:] in ("major", "minor", "patch"):
            part = a[2:]
    with open(MANIFEST, "rb") as f:
        raw = f.read()
    nl = b"\r\n" if b"\r\n" in raw else b"\n"
    m = re.search(rb"^(version\s*=\s*)(\d+(?:\.\d+)*)", raw, re.M)
    if not m:
        raise SystemExit("manifest 中未找到 version 行")
    old = m.group(2).decode()
    new = bump(old, part)
    raw = raw[:m.start(2)] + new.encode() + raw[m.end(2):]
    with open(MANIFEST, "wb") as f:
        f.write(raw)
    print("[bump] %s -> %s" % (old, new), flush=True)
    out = os.environ.get("GITHUB_OUTPUT")
    if out:
        with open(out, "a", encoding="utf-8") as f:
            f.write("version=%s\n" % new)
            f.write("old=%s\n" % old)


if __name__ == "__main__":
    main()
