#!/usr/bin/env python3
"""Keeps internal/runtime/<product>_versions.json in sync with upstream releases.

Usage: check-versions.py php|node|python|go|ruby

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

PRODUCTS = {
    "php": {"file": "php_versions.json", "eol_api": "https://endoflife.date/api/php.json", "hub": "php", "min": (7, 4),
            "stable": "{c}-fpm", "preview": "{c}-rc-fpm", "base_stable": "{c}", "base_preview": "{c}-rc", "label": None},
    "node": {"file": "node_versions.json", "eol_api": "https://endoflife.date/api/nodejs.json", "hub": "node", "min": (18,),
             "stable": "{c}-bookworm-slim", "preview": None, "base_stable": "{c}-bookworm-slim", "base_preview": None, "label": "node"},
    "python": {"file": "python_versions.json", "eol_api": "https://endoflife.date/api/python.json", "hub": "python", "min": (3, 10),
               "stable": "{c}-slim-bookworm", "preview": "{c}-rc-slim-bookworm", "base_stable": "{c}-slim-bookworm", "base_preview": "{c}-rc-slim-bookworm", "label": None},
    # Go supports its two newest releases (older ones stay, marked eol, like the other
    # runtimes); release candidates have no cycle tag to follow.
    "go": {"file": "go_versions.json", "eol_api": "https://endoflife.date/api/go.json", "hub": "golang", "min": (1, 26),
           "stable": "{c}-bookworm", "preview": None, "base_stable": "{c}-bookworm", "base_preview": None, "label": None},
    # Ruby's previews are tagged per release (4.1.0-preview1), not per cycle: only stable
    # cycles are followed.
    "ruby": {"file": "ruby_versions.json", "eol_api": "https://endoflife.date/api/ruby.json", "hub": "ruby", "min": (3, 3),
             "stable": "{c}-slim-bookworm", "preview": None, "base_stable": "{c}-slim-bookworm", "base_preview": None, "label": None},
}
PRODUCT = PRODUCTS[sys.argv[1] if len(sys.argv) > 1 else "php"]
FILE = Path(__file__).resolve().parent.parent / "internal/runtime" / PRODUCT["file"]
MIN_VERSION = PRODUCT["min"]
HUB = f"https://hub.docker.com/v2/repositories/library/{PRODUCT['hub']}/tags/"


def get(url: str):
    req = urllib.request.Request(url, headers={"User-Agent": "envoryx-version-check"})
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
    _, body = get(PRODUCT["eol_api"])
    cycles = json.loads(body)
    today = date.today().isoformat()

    versions = []
    for c in cycles:
        cycle = str(c["cycle"])
        if parse(cycle) < MIN_VERSION:
            continue
        eol = c.get("eol")
        is_eol = isinstance(eol, str) and eol < today
        if tag_exists(PRODUCT["stable"].format(c=cycle)):
            lts = c.get("lts")
            is_lts = isinstance(lts, str) and lts <= today  # a future date means "LTS later"
            if PRODUCT["label"] == "node" and is_eol and not isinstance(lts, str):
                continue  # odd "current" releases that reached EOL are of no use
            versions.append({"version": cycle, "base": PRODUCT["base_stable"].format(c=cycle), "eol": is_eol, "preview": False, "lts": is_lts})
        elif PRODUCT["preview"] and tag_exists(PRODUCT["preview"].format(c=cycle)):
            versions.append({"version": cycle, "base": PRODUCT["base_preview"].format(c=cycle), "eol": False, "preview": True, "lts": False})

    # endoflife.date may not list the next cycle yet: probe one minor above the newest.
    newest = max((parse(v["version"]) for v in versions), default=MIN_VERSION)
    nxt = f"{newest[0]}.{newest[1] + 1}" if len(newest) > 1 else f"{newest[0] + 1}"
    if all(v["version"] != nxt for v in versions):
        if tag_exists(PRODUCT["stable"].format(c=nxt)):
            versions.append({"version": nxt, "base": PRODUCT["base_stable"].format(c=nxt), "eol": False, "preview": False, "lts": False})
        elif PRODUCT["preview"] and tag_exists(PRODUCT["preview"].format(c=nxt)):
            versions.append({"version": nxt, "base": PRODUCT["base_preview"].format(c=nxt), "eol": False, "preview": True, "lts": False})

    versions.sort(key=lambda v: parse(v["version"]), reverse=True)
    stable = [v for v in versions if not v["preview"] and not v["eol"]]
    if PRODUCT["label"] == "node":
        # Node: default to the newest LTS line; odd releases are "current" and not LTS.
        lts = [v for v in stable if v.get("lts")]
        if lts:
            stable = lts + [v for v in stable if v not in lts]
    if not stable:
        print("no stable version found, refusing to update", file=sys.stderr)
        print("changed=false")
        return 0

    out = {"image": current["image"], "default": stable[0]["version"], "versions": []}
    for v in versions:
        entry = {"version": v["version"], "base": v["base"]}
        if PRODUCT["label"] == "node":
            entry["label"] = f"Node {v['version']} LTS" if v.get("lts") else f"Node {v['version']} (current)"
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
