"""Exercise deterministic packaging and draft-only upload behavior offline."""
import contextlib
import hashlib
import importlib.util
import io
import json
from pathlib import Path
import tarfile
import tempfile
import unittest
from unittest.mock import patch
import urllib.parse
import zipfile

import package

spec = importlib.util.spec_from_file_location("draft_release", Path(__file__).with_name("draft-release.py"))
draft = importlib.util.module_from_spec(spec)
spec.loader.exec_module(draft)


class DistributionTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.patch = patch.object(package, "ROOT", self.root)
        self.patch.start()
        self.addCleanup(self.patch.stop)
        for name in package.CONTRACT["documents"]:
            path = self.root / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text("Public fixture\n")
        (self.root / "bin").mkdir()
        for target in package.CONTRACT["targets"]:
            name = "easy-sso-" + target + (".exe" if target.startswith("windows-") else "")
            (self.root / "bin" / name).write_bytes(b"binary fixture")
        package.package("1.2.3")
        self.output = self.root / "bin/release"

    def test_reproducible_archives_and_channels(self):
        before = {p.name: p.read_bytes() for p in self.output.iterdir()}
        (self.output / "stale-archive.zip").write_bytes(b"stale")
        package.package("1.2.3")
        self.assertEqual(before, {p.name: p.read_bytes() for p in self.output.iterdir()})
        manifest = {}
        for line in (self.output / "SHA256SUMS").read_text().splitlines():
            digest, name = line.split("  ")
            self.assertEqual(digest, hashlib.sha256(before[name]).hexdigest())
            manifest[name] = digest
        self.assertEqual(set(before), set(manifest) | {"SHA256SUMS"})
        for name in manifest:
            if name.endswith(".zip"):
                with zipfile.ZipFile(self.output / name) as archive:
                    self.assertEqual(archive.getinfo("easysso.exe").external_attr >> 16, 0o100755)
                    self.assertEqual(json.loads(archive.read("easy-sso.install.json"))["channel"], "direct")
            else:
                with tarfile.open(self.output / name) as archive:
                    self.assertEqual(archive.getmember("easysso").mode, 0o755)
                    self.assertEqual(json.load(archive.extractfile("easy-sso.install.json"))["channel"], "direct")
        scoop = json.loads((self.root / "bin/channels/easy-sso.json").read_text())
        item = scoop["architecture"]["64bit"]
        self.assertEqual(item["hash"], manifest[item["url"].split("/")[-1]])
        self.assertIn('"channel": "scoop"', scoop["post_install"])
        brew = (self.root / "bin/channels/easy-sso.rb").read_text()
        for name, digest in manifest.items():
            if not name.endswith(".zip"):
                self.assertIn(name, brew)
                self.assertIn(digest, brew)
        self.assertIn('"channel": "homebrew"', brew)

    def test_draft_only_and_exact_uploads(self):
        calls, uploaded = [], []

        class Opener:
            def open(_, req, timeout):
                calls.append(req)
                if "/assets?" in req.full_url:
                    name = urllib.parse.parse_qs(urllib.parse.urlparse(req.full_url).query)["name"][0]
                    uploaded.append(name)
                    self.assertEqual(req.data, (self.output / name).read_bytes())
                    result = {}
                elif req.get_method() == "POST":
                    body = json.loads(req.data)
                    self.assertTrue(body["draft"])
                    self.assertEqual(body["make_latest"], "false")
                    self.assertEqual(body["tag_name"], "v1.2.3")
                    result = {"id": 7}
                elif "?per_page=" in req.full_url:
                    result = []
                else:
                    result = {"draft": True, "assets": [{"name": n} for n in uploaded], "html_url": "https://example.com/draft"}
                return io.BytesIO(json.dumps(result).encode())

        with patch.dict("os.environ", {"GH_TOKEN": "fixture-token"}), patch("sys.argv", ["draft-release.py", "v1.2.3", "unreleased", "--directory", str(self.output)]), patch.object(draft.urllib.request, "build_opener", return_value=Opener()):
            with contextlib.redirect_stdout(io.StringIO()):
                draft.main()
        self.assertEqual(set(uploaded), {p.name for p in self.output.iterdir()})
        self.assertEqual(len(uploaded), 6)

    def test_reject_invalid_versions(self):
        for value in ("1.2", "vv1.2.3", "01.2.3", "1.2.3-01", "1.2.3-a.+b", "1.2.3+a..b"):
            with self.assertRaises(ValueError):
                package.version(value)


if __name__ == "__main__":
    unittest.main()
