#!/usr/bin/env python3
"""Flag common private metadata in publishable files without printing its value.

With --history, also scan every blob and commit identity reachable from local
refs, including remote-tracking refs. With --revs, scan only the blobs and
commits of that revision range, such as the commits a push would publish.
Hosting caches require separate cleanup.

Company, profile and other private names come from a local denylist outside
the repository (EASY_SSO_PUBLIC_DENYLIST, default
~/.config/easy-sso-dev/public-denylist): one case-insensitive term per line,
# for comments. Matches are reported without the term or the file path.
"""
import argparse
import os
from pathlib import Path
import re
import subprocess
import sys

ROOT = Path(__file__).resolve().parent.parent
SYNTHETIC_ACCOUNTS = {
    "000000000000", "000011112222", "111111111111", "111122223333",
    "123456789012", "222222222222", "444455556666", "999999999999",
}
RULES = {
    "old project name": re.compile(r"easy[-_]" + "ssm", re.I),
    "AWS access key": re.compile(r"\b(?:AKIA|ASIA)[A-Z0-9]{16}\b"),
    "private key": re.compile(r"-----BEGIN (?:RSA |EC |OPENSSH )?PRIVATE KEY-----"),
    "personal home path": re.compile(r"/(?:home|Users)/[A-Za-z0-9_.-]+/"),
    "Windows home path": re.compile(r"[A-Z]:[\\/]Users[\\/][A-Za-z0-9_.-]+[\\/]", re.I),
}
ACCOUNT = re.compile(r"(?<![\w])[0-9]{12}(?![\w])")
EMAIL = re.compile(r"\b[\w.+-]+@[\w.-]+\.[A-Za-z]{2,}\b")
PORTAL = re.compile(r"https://([\w.-]+)\.awsapps\.com\b", re.I)
SSO_URL = re.compile(r"sso_start_url\s*=\s*https?://([^/\s\\]+)", re.I)
DENYLIST = Path(os.environ.get("EASY_SSO_PUBLIC_DENYLIST") or Path.home() / ".config/easy-sso-dev/public-denylist")


def git(*args):
    return subprocess.check_output(["git", *args], cwd=ROOT)


def example_host(host):
    return any(host == suffix or host.endswith("." + suffix)
               for suffix in ("example.com", "example.invalid", "example.awsapps.com"))


def public_email(address):
    host = address.rsplit("@", 1)[1].lower()
    return (example_host(host) or host in ("easy-sso.invalid", "users.noreply.github.com")
            or address.lower() == "noreply@github.com")


def load_denylist():
    path = DENYLIST.expanduser().resolve()
    if path == ROOT or ROOT in path.parents:
        print("Private denylist must live outside the repository so its names never reach Git.")
        sys.exit(2)
    if not path.is_file():
        print(f"Note: private denylist not configured ({DENYLIST}); company-specific names are not checked.")
        return []
    lines = (line.strip().lower() for line in path.read_text(encoding="utf-8").splitlines())
    return [line for line in lines if line and not line.startswith("#")]


def denied(text, terms):
    text = text.lower()
    return any(term in text for term in terms)


def scan(label, data, terms):
    failures = 0
    if denied(label, terms):
        label = "[path withheld]"
        print(f"{label}: private denylist term in path")
        failures += 1
    if b"\x00" in data:
        # Binary assets need manual inspection; report their existence explicitly.
        print(f"REVIEW binary: {label}")
        return failures
    for number, line in enumerate(data.decode("utf-8", errors="replace").splitlines(), 1):
        reasons = [name for name, rule in RULES.items() if rule.search(line)]
        if denied(line, terms):
            reasons.append("private denylist term")
        if any(value not in SYNTHETIC_ACCOUNTS for value in ACCOUNT.findall(line)):
            reasons.append("non-synthetic AWS account ID")
        if any(not public_email(address) for address in EMAIL.findall(line)):
            reasons.append("non-example email address")
        if any(not example_host(host + ".awsapps.com") for host in PORTAL.findall(line)):
            reasons.append("non-example SSO portal")
        if any(not example_host(host) for host in SSO_URL.findall(line)):
            reasons.append("non-example SSO configuration URL")
        if reasons:
            print(f"{label}:{number}: {', '.join(reasons)}")
            failures += 1
    return failures


def scan_history(revs, terms):
    failures = 0
    objects = git("rev-list", "--objects", *revs).splitlines()
    for item in objects:
        oid, _, name = item.partition(b" ")
        if git("cat-file", "-t", oid.decode()).strip() == b"blob":
            failures += scan(f"history:{oid.decode()[:12]}:{name.decode()}", git("cat-file", "blob", oid.decode()), terms)
    # A GitHub noreply author or committer is a public identity; its display
    # name may match a private term, so only the email rules apply to it.
    for line in git("log", *revs, "--format=%an <%ae>%n%cn <%ce>").decode().splitlines():
        public = line.lower().endswith("@users.noreply.github.com>")
        failures += scan("commit identity", line.encode(), [] if public else terms)
    return failures + scan("commit metadata", git("log", *revs, "--format=%B"), terms)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--history", action="store_true")
    parser.add_argument("--revs", help="scan only this revision range, e.g. 'origin/main..HEAD'")
    args = parser.parse_args()
    terms = load_denylist()
    failures = 0
    if args.revs:
        failures += scan_history(args.revs.split(), terms)
    else:
        paths = git("ls-files", "-z", "--cached", "--others", "--exclude-standard").split(b"\x00")
        for name in sorted(set(paths)):
            if not name:
                continue
            path = ROOT / name.decode()
            if path.is_file():
                failures += scan(name.decode(), path.read_bytes(), terms)
    if args.history:
        failures += scan_history(["--all"], terms)
    if failures:
        print(f"Public-data check failed: {failures} finding(s). Values omitted.")
        return 1
    print("Public-data checks passed (heuristic checks; review binary assets separately).")
    return 0


if __name__ == "__main__":
    sys.exit(main())
