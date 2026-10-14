#!/usr/bin/env python3
"""Create a draft only, then upload exactly the contract's archives and manifest."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import urllib.error
import urllib.parse
import urllib.request
from package import CONTRACT, asset, version


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("tag")
    parser.add_argument("commit")
    parser.add_argument("--directory", default="bin/release")
    args = parser.parse_args()
    ver = version(args.tag)
    if args.tag != "v" + ver:
        parser.error("tag must be canonical v-prefixed SemVer")
    root = Path(args.directory)
    names = {asset(ver, t) for t in CONTRACT["targets"]}
    if {p.name for p in root.iterdir()} != names | {"SHA256SUMS"}:
        parser.error("release directory must contain exactly the five archives and SHA256SUMS")
    expected = "".join(f"{hashlib.sha256((root / n).read_bytes()).hexdigest()}  {n}\n" for n in sorted(names))
    if (root / "SHA256SUMS").read_text() != expected:
        parser.error("checksum manifest does not match release artifacts")
    token = os.environ["GH_TOKEN"]
    repo = CONTRACT["repository"]
    api = f"https://api.github.com/repos/{repo}"

    class NoRedirect(urllib.request.HTTPRedirectHandler):
        def redirect_request(self, req, fp, code, msg, headers, newurl):
            return None

    opener = urllib.request.build_opener(NoRedirect())

    def request(url, data, content_type="application/json"):
        req = urllib.request.Request(url, data=data, headers={"Authorization": "Bearer " + token,
            "Accept": "application/vnd.github+json", "Content-Type": content_type,
            "X-GitHub-Api-Version": "2022-11-28", "User-Agent": "easy-sso-release"})
        try:
            with opener.open(req, timeout=120) as response:
                return json.load(response)
        except urllib.error.HTTPError as exc:
            raise SystemExit(f"GitHub release operation failed with HTTP {exc.code}; inspect the draft before retrying") from None
        except urllib.error.URLError:
            raise SystemExit("GitHub release operation failed; inspect the draft before retrying") from None

    # Include drafts, so reruns cannot silently create duplicate draft releases.
    for page in range(1, 11):
        listing = request(api + f"/releases?per_page=100&page={page}", None)
        if any(r["tag_name"] == args.tag for r in listing):
            raise SystemExit("release already exists; review/delete the incomplete draft explicitly before retrying")
        if len(listing) < 100:
            break
    else:
        raise SystemExit("release list exceeded safety bound")
    release = request(api + "/releases", json.dumps({"tag_name": args.tag, "target_commitish": args.commit,
        "name": args.tag, "draft": True, "prerelease": "-" in ver.split("+", 1)[0], "make_latest": "false",
        "body": "Draft for review. Native macOS/Windows and live AWS qualification remain required. "
                "SHA256SUMS covers the five archives; checksums are not independent publisher signatures. "
                "See bundled docs/DISTRIBUTION.md and docs/VALIDATION.md."}).encode())
    for name in sorted(names | {"SHA256SUMS"}):
        url = f'https://uploads.github.com/repos/{repo}/releases/{release["id"]}/assets?name={urllib.parse.quote(name)}'
        request(url, (root / name).read_bytes(), "application/octet-stream")
    uploaded = request(api + f'/releases/{release["id"]}', None)
    if not uploaded["draft"] or {a["name"] for a in uploaded["assets"]} != names | {"SHA256SUMS"}:
        raise SystemExit("draft verification failed; inspect release manually")
    print("Created reviewable draft: " + uploaded["html_url"])


if __name__ == "__main__":
    main()
