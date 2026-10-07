#!/usr/bin/env python3
"""Local development journal: screenshots, videos and a summary per entry,
browsable as a static HTML page (devlog/index.html) in any project.

  devlog.py add --title "..." --summary "..." --media a.png b.mp4
  devlog.py add --title "..." --summary-file notes.md --media shots/*.png
  devlog.py build          # regenerate index.html
  devlog.py open           # open index.html in the browser
  devlog.py list           # print entries

Media files are copied into devlog/entries/<id>/, so later captures that
overwrite the originals don't change the history. devlog/ is added to
.gitignore (local only) unless --keep-in-git is given.
"""
import argparse, datetime, html, json, os, re, shutil, subprocess, sys
from pathlib import Path

IMAGE = {".png", ".jpg", ".jpeg", ".gif", ".webp", ".svg"}
VIDEO = {".mp4", ".webm", ".mov", ".m4v"}


def git(root, *args):
    try:
        return subprocess.run(["git", "-C", str(root), *args], capture_output=True, text=True, check=True).stdout.strip()
    except Exception:
        return ""


def find_root(arg):
    if arg:
        return Path(arg).resolve()
    top = git(Path.cwd(), "rev-parse", "--show-toplevel")
    return Path(top) if top else Path.cwd()


def ensure_ignored(root):
    if not git(root, "rev-parse", "--git-dir"):
        return
    r = subprocess.run(["git", "-C", str(root), "check-ignore", "-q", "devlog/x"])
    if r.returncode == 0:
        return
    gi = root / ".gitignore"
    text = gi.read_text() if gi.exists() else ""
    sep = "" if text == "" or text.endswith("\n") else "\n"
    gi.write_text(text + sep + "\n# Local development journal (devlog skill)\n/devlog/\n")
    print("devlog: added /devlog/ to .gitignore")


def slugify(title):
    s = re.sub(r"[^\w\-]+", "-", title, flags=re.UNICODE).strip("-").lower()
    return s[:40] or "entry"


def cmd_add(a):
    root = find_root(a.root)
    log = root / "devlog"
    now = datetime.datetime.now().astimezone()
    eid = now.strftime("%Y%m%d-%H%M%S") + "-" + slugify(a.title)
    edir = log / "entries" / eid
    edir.mkdir(parents=True, exist_ok=False)
    summary = a.summary or ""
    if a.summary_file:
        summary = Path(a.summary_file).read_text()
    media = []
    used = set()
    for m in a.media or []:
        src = Path(m)
        if not src.is_file():
            print(f"devlog: skip missing {m}", file=sys.stderr)
            continue
        name = src.name
        if name in used:  # frame.png from two folders
            name = f"{src.parent.name}-{name}"
        used.add(name)
        shutil.copy2(src, edir / name)
        kind = "video" if src.suffix.lower() in VIDEO else "image" if src.suffix.lower() in IMAGE else "file"
        # Keep only a project-relative path: absolute paths would leak the user's home layout.
        try:
            source = str(src.resolve().relative_to(root))
        except ValueError:
            source = src.name
        media.append({"file": name, "kind": kind, "source": source})
    dirty = bool(git(root, "status", "--porcelain"))
    entry = {
        "id": eid,
        "title": a.title,
        "time": now.isoformat(timespec="seconds"),
        # Creation order, for entries added within the same second.
        "seq": len(list((log / "entries").glob("*/entry.json"))),
        "summary": summary,
        "media": media,
        "tags": a.tags or [],
        "git": {"branch": git(root, "rev-parse", "--abbrev-ref", "HEAD"), "commit": git(root, "rev-parse", "--short", "HEAD"), "dirty": dirty},
    }
    (edir / "entry.json").write_text(json.dumps(entry, ensure_ascii=False, indent=2))
    if not a.keep_in_git:
        ensure_ignored(root)
    build(root)
    print(f"devlog: added {eid} ({len(media)} media)\n{log / 'index.html'}")


def load_entries(log):
    out = []
    for f in sorted((log / "entries").glob("*/entry.json")):
        try:
            out.append(json.loads(f.read_text()))
        except Exception as e:
            print(f"devlog: bad {f}: {e}", file=sys.stderr)
    out.sort(key=lambda e: (e["time"], e.get("seq", 0)), reverse=True)
    return out


def inline(s):
    s = html.escape(s)
    s = re.sub(r"`([^`]+)`", r"<code>\1</code>", s)
    s = re.sub(r"\*\*([^*]+)\*\*", r"<strong>\1</strong>", s)
    s = re.sub(r"(https?://[^\s<]+)", r'<a href="\1">\1</a>', s)
    return s


def markdown(text):
    """Headings, bullet lists and paragraphs - enough for a summary."""
    out, items, para = [], [], []

    def flush():
        if items:
            out.append("<ul>" + "".join(f"<li>{inline(i)}</li>" for i in items) + "</ul>")
            items.clear()
        if para:
            out.append("<p>" + "<br>".join(inline(p) for p in para) + "</p>")
            para.clear()

    for line in text.splitlines():
        st = line.strip()
        m = re.match(r"^(#{1,4})\s+(.*)", st)
        if not st:
            flush()
        elif m:
            flush()
            out.append(f"<h4>{inline(m.group(2))}</h4>")
        elif re.match(r"^[-*・]\s+", st):
            if para:
                flush()
            items.append(re.sub(r"^[-*・]\s+", "", st))
        elif items and line.startswith(("  ", "\t")):
            items[-1] += " " + st
        else:
            if items:
                flush()
            para.append(st)
    flush()
    return "\n".join(out)


CSS = """
:root{--bg:#f6f4ef;--card:#fff;--fg:#26232b;--muted:#6f6a78;--line:#e3dfd6;--accent:#6b4fd8;--code:#f0ede6}
@media (prefers-color-scheme:dark){:root{--bg:#18161c;--card:#222029;--fg:#ece9f1;--muted:#a19cab;--line:#34313c;--accent:#a996ff;--code:#2c2934}}
*{box-sizing:border-box}body{margin:0;background:var(--bg);color:var(--fg);font:15px/1.65 -apple-system,BlinkMacSystemFont,"Hiragino Sans","Noto Sans JP",sans-serif}
header{position:sticky;top:0;z-index:2;background:var(--bg);border-bottom:1px solid var(--line);padding:14px 16px}
.wrap{max-width:1100px;margin:0 auto}h1{font-size:20px;margin:0 0 8px}
input{width:100%;padding:8px 12px;border:1px solid var(--line);border-radius:8px;background:var(--card);color:var(--fg);font:inherit}
main{padding:16px}.day{color:var(--muted);font-size:13px;font-weight:600;margin:28px 0 8px;letter-spacing:.04em}
article{background:var(--card);border:1px solid var(--line);border-radius:12px;padding:16px 18px;margin:0 0 14px}
article h2{font-size:17px;margin:0 0 4px}.meta{color:var(--muted);font-size:12.5px;margin-bottom:8px}
.meta code,.summary code{background:var(--code);padding:1px 5px;border-radius:4px;font-size:.92em}
.summary h4{margin:12px 0 4px;font-size:14.5px}.summary p,.summary ul{margin:6px 0}.summary ul{padding-left:1.3em}
.tag{display:inline-block;border:1px solid var(--line);border-radius:99px;padding:0 8px;margin-left:4px;font-size:12px}
.media{display:grid;grid-template-columns:repeat(auto-fill,minmax(260px,1fr));gap:10px;margin-top:12px}
.media figure{margin:0}.media img,.media video{width:100%;border-radius:8px;border:1px solid var(--line);background:#000;display:block}
.media figcaption{color:var(--muted);font-size:12px;margin-top:3px;word-break:break-all}
.media .wide{grid-column:1/-1}.media .wide video{max-width:760px}a{color:var(--accent)}.empty{color:var(--muted);text-align:center;padding:40px}
#more{display:block;margin:8px auto 24px;padding:10px 28px;border:1px solid var(--line);border-radius:99px;background:var(--card);color:var(--accent);font:inherit;cursor:pointer}#more:hover{border-color:var(--accent)}#more[hidden]{display:none}
"""

PAGE_SIZE = 3

# Entries live in inert <template>s (their images/videos aren't fetched) and
# are moved into the page PAGE_SIZE at a time; search covers all of them.
JS = """
const q=document.getElementById('q'),list=document.getElementById('list'),more=document.getElementById('more');
const tpls=[...document.querySelectorAll('template.entry')],STEP=+list.dataset.step;
let matches=tpls,shown=0,lastDay=null;
const node=t=>t._n||(t._n=t.content.firstElementChild.cloneNode(true));
function showMore(){const end=Math.min(shown+STEP,matches.length);
for(;shown<end;shown++){const t=matches[shown];
if(t.dataset.day!==lastDay){const d=document.createElement('div');d.className='day';d.textContent=lastDay=t.dataset.day;list.append(d)}
list.append(node(t))}
more.hidden=shown>=matches.length;more.textContent=`もっと読む（残り ${matches.length-shown} 件）`}
function reset(){const s=q.value.toLowerCase();matches=tpls.filter(t=>t.dataset.text.includes(s));
list.replaceChildren();shown=0;lastDay=null;showMore();
if(!matches.length&&tpls.length)list.innerHTML='<p class="empty">一致する記録がありません</p>'}
q.addEventListener('input',reset);more.addEventListener('click',showMore);reset();
"""


def build(root):
    log = root / "devlog"
    entries = load_entries(log)
    project = html.escape(root.name)
    parts = []
    for e in entries:
        t = datetime.datetime.fromisoformat(e["time"])
        d = t.strftime("%Y-%m-%d (%a)")
        g = e.get("git", {})
        gitinfo = f'<code>{html.escape(g.get("commit",""))}</code>{" + 未コミットの変更" if g.get("dirty") else ""}' if g.get("commit") else ""
        tags = "".join(f'<span class="tag">{html.escape(x)}</span>' for x in e.get("tags", []))
        media = []
        for m in e.get("media", []):
            src = html.escape(f'entries/{e["id"]}/{m["file"]}')
            cap = f'<figcaption>{html.escape(m["file"])}</figcaption>'
            if m["kind"] == "video":
                media.append(f'<figure class="wide"><video src="{src}#t=0.1" controls preload="metadata"></video>{cap}</figure>')
            elif m["kind"] == "image":
                media.append(f'<figure><a href="{src}" target="_blank"><img src="{src}" loading="lazy" alt=""></a>{cap}</figure>')
            else:
                media.append(f'<figure><a href="{src}">{html.escape(m["file"])}</a></figure>')
        text = html.escape((e["title"] + " " + e.get("summary", "") + " " + " ".join(e.get("tags", []))).lower(), quote=True)
        parts.append(
            f'<template class="entry" data-day="{d}" data-text="{text}"><article><h2>{html.escape(e["title"])}{tags}</h2>'
            f'<div class="meta">{t.strftime("%H:%M")} · {html.escape(g.get("branch",""))} {gitinfo}</div>'
            f'<div class="summary">{markdown(e.get("summary",""))}</div>'
            + (f'<div class="media">{"".join(media)}</div>' if media else "") + "</article></template>")
    body = "\n".join(parts) if parts else '<p class="empty">まだ記録がありません</p>'
    page = f"""<!doctype html><html lang="ja"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>{project} 開発履歴</title><style>{CSS}</style></head><body>
<header><div class="wrap"><h1>{project} 開発履歴 <span style="color:var(--muted);font-size:13px;font-weight:400">{len(entries)} 件</span></h1>
<input id="q" type="search" placeholder="絞り込み（タイトル・概要・タグ）"></div></header>
<main><div class="wrap">{body}<div id="list" data-step="{PAGE_SIZE}"></div>
<button id="more" hidden></button></div></main><script>{JS}</script></body></html>"""
    log.mkdir(exist_ok=True)
    (log / "index.html").write_text(page)


def main():
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    p.add_argument("--root", help="project root (default: git top level or cwd)")
    sub = p.add_subparsers(dest="cmd", required=True)
    a = sub.add_parser("add")
    a.add_argument("--title", required=True)
    a.add_argument("--summary")
    a.add_argument("--summary-file")
    a.add_argument("--media", nargs="*")
    a.add_argument("--tags", nargs="*")
    a.add_argument("--keep-in-git", action="store_true", help="don't add devlog/ to .gitignore")
    sub.add_parser("build")
    sub.add_parser("open")
    sub.add_parser("list")
    args = p.parse_args()
    if args.cmd == "add":
        return cmd_add(args)
    root = find_root(args.root)
    if args.cmd == "build":
        build(root)
        print(root / "devlog" / "index.html")
    elif args.cmd == "open":
        build(root)
        subprocess.run(["open" if sys.platform == "darwin" else "xdg-open", str(root / "devlog" / "index.html")])
    elif args.cmd == "list":
        for e in load_entries(root / "devlog"):
            print(e["time"], e["id"], len(e.get("media", [])), "media")


if __name__ == "__main__":
    main()
