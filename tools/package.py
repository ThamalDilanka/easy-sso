#!/usr/bin/env python3
"""Build reproducible release archives and candidate channel manifests."""
import argparse
import gzip
import hashlib
import io
import json
import os
from pathlib import Path
import re
import subprocess
import tarfile
import zipfile

ROOT = Path(__file__).resolve().parent.parent
CONTRACT = json.loads((ROOT / "internal/releases/contract.json").read_text())


def version(value):
    if not re.fullmatch(CONTRACT["version_pattern"], value):
        raise ValueError("version must be a complete semantic version")
    value = value.removeprefix("v")
    base, _, metadata = value.partition("+")
    pre = base.partition("-")[2]
    for part in (pre, metadata):
        if part and any(not p for p in part.split(".")):
            raise ValueError("empty version identifier")
    if any(p.isdigit() and len(p) > 1 and p[0] == "0" for p in pre.split(".")):
        raise ValueError("leading zero in prerelease")
    return value


def asset(ver, target):
    return CONTRACT["archive_pattern"].format(version=ver, target=target) + CONTRACT["archive_extensions"][target.split("-")[0]]


def build(ver, commit):
    for target in CONTRACT["targets"]:
        goos, arch = target.split("-")
        output = ROOT / "bin" / (f"easy-sso-{target}" + (".exe" if goos == "windows" else ""))
        subprocess.run([os.environ.get("GO", "go"), "build", "-trimpath", "-ldflags",
                        f"-X main.version={ver} -X main.commit={commit} -X main.distribution=release "
                        f"-X github.com/ThamalDilanka/easy-sso/internal/releases.Stamp=easy-sso-release-v1|{ver}|{goos}|{arch}|{commit}|end",
                        "-o", str(output), "./cmd/easysso"], cwd=ROOT,
                       env={**os.environ, "GOOS": goos, "GOARCH": arch, "CGO_ENABLED": "0"}, check=True)


def package(ver):
    output_dir = ROOT / "bin" / "release"
    output_dir.mkdir(parents=True, exist_ok=True)
    # Refuse stale assets in the upload directory: each invocation has one version.
    for path in output_dir.iterdir():
        if path.is_file():
            path.unlink()
        else:
            raise ValueError("unexpected directory in bin/release")
    sums = {}
    for target in CONTRACT["targets"]:
        windows = target.startswith("windows-")
        binary = "easysso.exe" if windows else "easysso"
        built = ROOT / "bin" / (f"easy-sso-{target}" + (".exe" if windows else ""))
        entries = [(binary, built.read_bytes(), 0o755)]
        entries += [(p, (ROOT / p).read_bytes(), 0o644) for p in CONTRACT["documents"]]
        entries.append(("easy-sso.install.json", json.dumps({"channel": "direct", "repository": CONTRACT["repository"]}, sort_keys=True).encode() + b"\n", 0o644))
        output = output_dir / asset(ver, target)
        if windows:
            with zipfile.ZipFile(output, "w", compression=zipfile.ZIP_DEFLATED) as archive:
                for name, data, mode in entries:
                    info = zipfile.ZipInfo(name, (1980, 1, 1, 0, 0, 0))
                    info.create_system = 3
                    info.external_attr = (0o100000 | mode) << 16
                    info.compress_type = zipfile.ZIP_DEFLATED
                    archive.writestr(info, data)
        else:
            payload = io.BytesIO()
            with tarfile.open(fileobj=payload, mode="w", format=tarfile.USTAR_FORMAT) as archive:
                for name, data, mode in entries:
                    info = tarfile.TarInfo(name)
                    info.size, info.mode, info.mtime = len(data), mode, 0
                    archive.addfile(info, io.BytesIO(data))
            with output.open("wb") as destination:
                with gzip.GzipFile(filename="", mode="wb", fileobj=destination, mtime=0) as zipped:
                    zipped.write(payload.getvalue())
        sums[output.name] = hashlib.sha256(output.read_bytes()).hexdigest()
    (output_dir / "SHA256SUMS").write_text("".join(f"{digest}  {name}\n" for name, digest in sorted(sums.items())))
    channels(ver, sums)


def channels(ver, sums):
    dest = ROOT / "bin" / "channels"
    dest.mkdir(parents=True, exist_ok=True)
    base = f'https://github.com/{CONTRACT["repository"]}/releases/download/v{ver}/'
    marker = json.dumps({"channel": "homebrew", "repository": CONTRACT["repository"]})
    formula = ['class EasySso < Formula', '  desc "AWS SSO roles and SSM connections in a terminal workspace"',
               f'  homepage "https://github.com/{CONTRACT["repository"]}"', f'  version "{ver}"', '  license "MIT"']
    for system, goos in [("macos", "darwin"), ("linux", "linux")]:
        formula.append(f"  on_{system} do")
        for cpu, arch in [("arm", "arm64"), ("intel", "amd64")]:
            name = asset(ver, f"{goos}-{arch}")
            formula += [f"    on_{cpu} do", f'      url "{base}{name}"', f'      sha256 "{sums[name]}"', "    end"]
        formula.append("  end")
    formula += ["  def install", '    libexec.install "easysso"', '    doc.install "LICENSE", "README.md", "docs"', f'    (libexec/"easy-sso.install.json").write \'{marker}\'',
                '    bin.install_symlink libexec/"easysso"', "  end", "  test do", '    assert_match version.to_s, shell_output("#{bin}/easysso version")', "  end", "end", ""]
    (dest / "easy-sso.rb").write_text("\n".join(formula))
    name = asset(ver, "windows-amd64")
    scoop = {"version": ver, "description": "AWS SSO roles and SSM connections in a terminal workspace",
             "homepage": f'https://github.com/{CONTRACT["repository"]}', "license": "MIT",
             "architecture": {"64bit": {"url": base + name, "hash": sums[name]}}, "bin": "easysso.exe",
             "post_install": "Set-Content -LiteralPath \"$dir\\easy-sso.install.json\" -Encoding ASCII -Value '" + json.dumps({"channel": "scoop", "repository": CONTRACT["repository"]}) + "'"}
    (dest / "easy-sso.json").write_text(json.dumps(scoop, indent=2) + "\n")


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("--version", default="0.1.0-dev")
    parser.add_argument("--commit", default="unreleased")
    parser.add_argument("--build-only", action="store_true")
    args = parser.parse_args()
    ver = version(args.version)
    if not re.fullmatch(r"[a-zA-Z0-9._-]+", args.commit):
        parser.error("invalid commit")
    build(ver, args.commit)
    if not args.build_only:
        package(ver)
        print("Created five archives and SHA256SUMS in bin/release; candidate manifests in bin/channels.")
