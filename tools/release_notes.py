#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""把「本次更新」写进两处：GitHub Release 说明 + fpk manifest 的 changelog 字段（应用中心里显示）。

用法：
  python3 tools/release_notes.py --version 1.0.20          # CI 用：生成 build/release_notes.md，并同步 manifest.changelog
  python3 tools/release_notes.py --version 1.0.20 --notes-only
  python3 tools/release_notes.py --sync-manifest           # 只按 CHANGELOG.md 重写 manifest.changelog
  python3 tools/release_notes.py --backfill                # 把 manifest 里已有的历史条目回填进 CHANGELOG.md

条目来源优先级：
  1) CHANGELOG.md 中 `## [x.y.z]` 小节；
  2) 缺失时用 `git log <上一 tag>..HEAD --no-merges` 的提交主题自动生成，并**写回 CHANGELOG.md**（所以不会漏写）。
"""

import argparse
import os
import datetime
import pathlib
import re
import shutil
import subprocess
import sys

ROOT = pathlib.Path(__file__).resolve().parent.parent
CHANGELOG = ROOT / "CHANGELOG.md"
MANIFEST = ROOT / "manifest"
NOTES_MD = ROOT / "build" / "release_notes.md"

# 纯内部/自动化提交不进更新日志
NOISE_RE = re.compile(r"^(chore\(release\)|Merge\b|Revert\b|Initial commit)", re.I)
KIND_MAP = (
    ("feat", "新增"),
    ("fix", "修复"),
    ("perf", "优化"),
    ("refactor", "优化"),
    ("docs", "文档"),
    ("test", "测试"),
)
MAX_CHANGELOG_LEN = 6000  # manifest 单行上限保护：超长时裁掉最旧版本


def read(p: pathlib.Path) -> str:
    if not p.exists():
        return ""
    with open(p, "r", encoding="utf-8", newline="") as f:
        return f.read()


def write(p: pathlib.Path, s: str) -> None:
    p.parent.mkdir(parents=True, exist_ok=True)
    with open(p, "w", encoding="utf-8", newline="") as f:
        f.write(s)


def git_exe() -> str:
    return os.environ.get("GIT_BIN") or shutil.which("git") or "git"


def git_available() -> bool:
    try:
        subprocess.run([git_exe(), "--version"], capture_output=True, check=True)
        return True
    except Exception:
        return False


def git(*args: str) -> str:
    try:
        out = subprocess.run(
            [git_exe(), "-c", "core.quotepath=false", *args], cwd=ROOT,
            capture_output=True, text=True, encoding="utf-8", errors="replace", check=True
        )
        return out.stdout
    except Exception:
        return ""


def today() -> str:
    epoch = git("log", "-1", "--format=%ct").strip()
    if epoch.isdigit():
        return datetime.datetime.fromtimestamp(int(epoch)).strftime("%Y-%m-%d")
    return datetime.date.today().strftime("%Y-%m-%d")


def prev_tag() -> str:
    return git("describe", "--tags", "--abbrev=0").strip()


# ---------- CHANGELOG.md ----------

def changelog_section(version: str) -> str | None:
    """取 CHANGELOG.md 中 `## [version]` 小节的正文（不含小节标题）。"""
    text = read(CHANGELOG)
    m = re.search(rf"^##\s*\[{re.escape(version)}\][^\n]*\n(.*?)(?=^##\s*\[|\Z)", text, re.M | re.S)
    if not m:
        return None
    return m.group(1).strip("\n")


def changelog_versions() -> list[str]:
    return re.findall(r"^##\s*\[(\d+\.\d+\.\d+)\]", read(CHANGELOG), re.M)


def bullet_kind(subject: str) -> str:
    low = subject.lower()
    for key, label in KIND_MAP:
        if low.startswith(key + ":") or low.startswith(key + "("):
            return label
    return "其他"


def auto_bullets() -> list[str]:
    """从上一 tag 到 HEAD 的提交主题生成条目。"""
    if not git_available():
        raise SystemExit("错误：找不到 git，无法从提交生成更新日志（可设环境变量 GIT_BIN 指向 git）")
    base = prev_tag()
    rng = f"{base}..HEAD" if base else "HEAD"
    subjects = [s.strip() for s in git("log", "--no-merges", "--pretty=format:%s", rng).splitlines()]
    bullets = []
    for s in subjects:
        if not s or NOISE_RE.match(s) or "[skip ci]" in s:
            continue
        # 去掉 "feat: " / "fix(scope): " 这类前缀，只留人类可读描述
        desc = re.sub(r"^\w+(\([^)]*\))?:\s*", "", s).strip()
        bullets.append(f"- **{bullet_kind(s)}**：{desc}")
    if not bullets:
        bullets = ["- 常规维护（无用户可见变更）"]
    return bullets


def insert_changelog(version: str, date: str, bullets: list[str]) -> None:
    text = read(CHANGELOG)
    if not text:
        text = "# 更新日志\n\n只记录**用户可见**的变更；发布时由 CI 自动追加。\n"
    nl = "\r\n" if "\r\n" in text else "\n"
    block = nl + f"## [{version}] - {date}" + nl + nl + nl.join(bullets) + nl
    lines = text.splitlines(keepends=True)
    idx = None
    for i, line in enumerate(lines):
        if re.match(r"^##\s*\[", line):
            idx = i
            break
    if idx is None:
        text = text.rstrip("\n") + "\n" + block
    else:
        text = "".join(lines[:idx]) + block.lstrip("\n") + "".join(lines[idx:])
    write(CHANGELOG, text)


# ---------- manifest ----------

def parse_manifest_changelog(text: str) -> list[tuple[str, str, str]]:
    m = re.search(r"^changelog\s*=\s*(.*)$", text, re.M)
    if not m:
        return []
    body = m.group(1).strip()
    entries = []
    for ver, date, desc in re.findall(
        r"(\d+\.\d+\.\d+):\s*(?:(\d{4}-\d{2}-\d{2})\s*)?(.*?)(?=\s+\d+\.\d+\.\d+:\s*(?:\d{4}-\d{2}-\d{2})?\s|\Z)",
        body,
        re.S,
    ):
        entries.append((ver, date or "", re.sub(r"\s+", " ", desc).strip()))
    return entries


def compose_manifest_changelog(entries: list[tuple[str, str, str]]) -> str:
    parts = []
    for ver, date, desc in entries:
        parts.append((f"{ver}: {date} {desc}".strip() if date else f"{ver}: {desc}").strip())
    out = " ".join(parts)
    while len(out) > MAX_CHANGELOG_LEN and len(parts) > 1:
        parts.pop()
        out = " ".join(parts)
    return out


def sync_manifest(version: str | None, date: str | None, desc: str | None) -> str:
    """把 CHANGELOG.md 的全部小节写回 manifest.changelog（version 给定时先把该版本放进 CHANGELOG）。"""
    if version and desc is not None and changelog_section(version) is None:
        insert_changelog(version, date or today(), desc.split("\n"))
    entries: list[tuple[str, str, str]] = []
    if version:
        sec = changelog_section(version)
        if sec:
            entries.append((version, date or today(), " ".join(sec.split())))
    known = {e[0] for e in entries}
    for ver in changelog_versions():
        if ver in known:
            continue
        sec = changelog_section(ver) or ""
        entries.append((ver, "", " ".join(sec.split())))
    if not entries:  # CHANGELOG 为空时保留 manifest 现有内容
        entries = parse_manifest_changelog(read(MANIFEST))
    line = "changelog             = " + compose_manifest_changelog(entries)
    text = read(MANIFEST)
    if re.search(r"^changelog\s*=.*$", text, re.M):
        text = re.sub(r"^changelog\s*=.*$", line, text, count=1, flags=re.M)
    else:
        text = text.rstrip("\r\n") + "\r\n" + line + "\r\n"
    write(MANIFEST, text)
    return line


# ---------- Release 说明 ----------

def render_notes(version: str, date: str, section: str) -> str:
    kern = "v1.19.32"
    return (
        f"显式代理（MihomoProxy）v{version} 自动构建产物。\n\n"
        f"## 本次更新（{date}）\n\n{section}\n\n"
        "## 安装\n\n"
        f"- 飞牛 fnOS → 应用中心 → 手动安装，选择下方的 `.fpk`（同一包内含 x86_64 与 aarch64）\n"
        f"- 包内已内置 mihomo 内核（{kern}）与 Web 控制台，无需额外下载；升级不会清除已有的订阅与规则\n"
    )


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--version", help="本次发布的版本号，如 1.0.20")
    ap.add_argument("--date", help="日期 YYYY-MM-DD，默认取最后一次提交时间")
    ap.add_argument("--notes-only", action="store_true", help="只生成 Release 说明，不动 manifest/CHANGELOG")
    ap.add_argument("--sync-manifest", action="store_true", help="只按 CHANGELOG.md 重写 manifest.changelog")
    ap.add_argument("--backfill", action="store_true", help="把 manifest 历史条目回填进 CHANGELOG.md")
    args = ap.parse_args()

    if args.backfill:
        added = 0
        for ver, date, desc in parse_manifest_changelog(read(MANIFEST)):
            if changelog_section(ver) is None and desc:
                insert_changelog(ver, date or today(), [f"- {desc}"])
                added += 1
        print(f"backfill: 新增 {added} 个历史小节到 CHANGELOG.md")
        return 0

    if args.sync_manifest and not args.version:
        line = sync_manifest(None, None, None)
        print("manifest.changelog =", line[:120], "...")
        return 0

    if not args.version:
        ap.error("需要 --version（或 --sync-manifest / --backfill）")

    date = args.date or today()
    section = changelog_section(args.version)
    if section is None:
        bullets = auto_bullets()
        section = "\n".join(bullets)
        if not args.notes_only:
            insert_changelog(args.version, date, bullets)
            print(f"CHANGELOG.md: 已自动追加 [{args.version}]（{len(bullets)} 条）")
    notes = render_notes(args.version, date, section)
    if not args.notes_only:
        write(NOTES_MD, notes)
        line = sync_manifest(args.version, date, section)
        print(f"Release 说明已写入 {NOTES_MD.relative_to(ROOT)}；manifest.changelog 已更新（{len(line)} 字符）")
    else:
        sys.stdout.write(notes)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
