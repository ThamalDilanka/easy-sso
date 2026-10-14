#!/usr/bin/env python3
"""Flag common private metadata in publishable files without printing its value.

With --history, also scan every blob and commit identity reachable from local
refs, including remote-tracking refs. Hosting caches require separate cleanup.
"""
import argparse
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


def git(*args):
    return subprocess.check_output(["git", *args], cwd=ROOT)


def example_host(host):
    return any(host == suffix or host.endswith("." + suffix)
               for suffix in ("example.com", "example.invalid", "example.awsapps.com"))


def public_email(address):
    host = address.rsplit("@", 1)[1].lower()
    return (example_host(host) or host in ("easy-sso.invalid", "users.noreply.github.com")
            or address.lower() == "noreply@github.com")


def scan(label, data):
    if b"\x00" in data:
        # Binary assets need manual inspection; report their existence explicitly.
        print(f"REVIEW binary: {label}")
        return 0
    failures = 0
    for number, line in enumerate(data.decode("utf-8", errors="replace").splitlines(), 1):
        reasons = [name for name, rule in RULES.items() if rule.search(line)]
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


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--history", action="store_true")
    args = parser.parse_args()
    failures = 0
    paths = git("ls-files", "-z", "--cached", "--others", "--exclude-standard").split(b"\x00")
    for name in sorted(set(paths)):
        if not name:
            continue
        path = ROOT / name.decode()
        if path.is_file():
            failures += scan(name.decode(), path.read_bytes())
    if args.history:
        objects = git("rev-list", "--objects", "--all").splitlines()
        for item in objects:
            oid, _, name = item.partition(b" ")
            if git("cat-file", "-t", oid.decode()).strip() == b"blob":
                failures += scan(f"history:{oid.decode()[:12]}:{name.decode()}", git("cat-file", "blob", oid.decode()))
        identities = git("log", "--all", "--format=%an <%ae>%n%cn <%ce>%n%B")
        failures += scan("commit metadata", identities)
    if failures:
        print(f"Public-data check failed: {failures} finding(s). Values omitted.")
        return 1
    print("Public-data checks passed (heuristic checks; review binary assets separately).")
    return 0


if __name__ == "__main__":
    sys.exit(main())
