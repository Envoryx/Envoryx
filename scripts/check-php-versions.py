#!/usr/bin/env python3
"""Keeps internal/runtime/php_versions.json in sync with upstream PHP releases.

Sources:
  - https://endoflife.date/api/php.json   release cycles and EOL dates
  - Docker Hub                             which php:<cycle>-fpm / -rc-fpm tags exist

Rules:
  - a cycle is listed when php:<cycle>-fpm exists (stable) or php:<cycle>-rc-fpm exists (preview)
  - eol = the cycle's EOL date is in the past
  - default = newest stable, non-EOL cycle
  - cycles older than MIN_VERSION are dropped

Exit code 0 always; prints "changed=true|false" for the workflow.
"""
import json
import sys
import urllib.request
from datetime import date
from pathlib import Path

FILE = Path(__file__).resolve().parent.parent / "internal/runtime/php_versions.json"
MIN_VERSION = (7, 4)
HUB = "https://hub.docker.com/v2/repositories/library/php/tags/"


def get(url: str):
    req = urllib.request.Request(url, headers={"User-Agent": "staqio-version-check"})
    with urllib.request.urlopen(req, timeout=30) as r:
        return r.status, r.read()


def tag_exists(tag: str) -> bool:
    try:
        status, _ = get(HUB + tag)
        return status == 200
    except Exception:
        return False


def parse(v: str):
    return tuple(int(x) for x in v.split("."))


def main() -> int:
    current = json.loads(FILE.read_text())
    _, body = get("https://endoflife.date/api/php.json")
    cycles = json.loads(body)
    today = date.today().isoformat()

    versions = []
    for c in cycles:
        cycle = str(c["cycle"])
        if parse(cycle) < MIN_VERSION:
            continue
        eol = c.get("eol")
        is_eol = isinstance(eol, str) and eol < today
        if tag_exists(f"{cycle}-fpm"):
            versions.append({"version": cycle, "base": cycle, "eol": is_eol, "preview": False})
        elif tag_exists(f"{cycle}-rc-fpm"):
            versions.append({"version": cycle, "base": f"{cycle}-rc", "eol": False, "preview": True})

    # endoflife.date may not list the next cycle yet: probe one minor above the newest.
    newest = max((parse(v["version"]) for v in versions), default=MIN_VERSION)
    nxt = f"{newest[0]}.{newest[1] + 1}"
    if all(v["version"] != nxt for v in versions):
        if tag_exists(f"{nxt}-fpm"):
            versions.append({"version": nxt, "base": nxt, "eol": False, "preview": False})
        elif tag_exists(f"{nxt}-rc-fpm"):
            versions.append({"version": nxt, "base": f"{nxt}-rc", "eol": False, "preview": True})

    versions.sort(key=lambda v: parse(v["version"]), reverse=True)
    stable = [v for v in versions if not v["preview"] and not v["eol"]]
    if not stable:
        print("no stable PHP version found, refusing to update", file=sys.stderr)
        print("changed=false")
        return 0

    out = {"image": current["image"], "default": stable[0]["version"], "versions": []}
    for v in versions:
        entry = {"version": v["version"], "base": v["base"]}
        if v["preview"]:
            entry["preview"] = True
        if v["eol"]:
            entry["eol"] = True
        out["versions"].append(entry)

    changed = out != current
    if changed:
        FILE.write_text(json.dumps(out, indent=2) + "\n")
    print(json.dumps(out, indent=2))
    print(f"changed={'true' if changed else 'false'}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
