"""The versioned pre-push hook refuses outgoing private data and fails closed."""
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest

TOOLS = Path(__file__).resolve().parent


class PrePushTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        base = Path(self.tmp.name)
        self.remote = base / "remote.git"
        self.repo = base / "repo"
        self.denylist = base / "public-denylist"
        self.denylist.write_text("Zyxample Corp\n")
        self.gitleaks = base / "gitleaks"
        subprocess.run(["git", "init", "-q", "--bare", str(self.remote)], check=True)
        (self.repo / "tools" / "hooks").mkdir(parents=True)
        shutil.copy(TOOLS / "check-public.py", self.repo / "tools")
        shutil.copy(TOOLS / "hooks" / "pre-push", self.repo / "tools" / "hooks")
        self.git("init", "-q", "-b", "main")
        self.git("config", "core.hooksPath", "tools/hooks")
        self.git("remote", "add", "origin", str(self.remote))
        self.fake_gitleaks(0)
        self.commit("README.md", "clean\n", "docs: start")
        self.assertEqual(self.push("main").returncode, 0)

    def tearDown(self):
        self.tmp.cleanup()

    def git(self, *args, check=True):
        return subprocess.run(
            ["git", "-c", "user.name=Example", "-c", "user.email=1+example@users.noreply.github.com", *args],
            cwd=self.repo, check=check, capture_output=True, text=True,
            env=dict(os.environ, EASY_SSO_PUBLIC_DENYLIST=str(self.denylist), EASY_SSO_GITLEAKS=str(self.gitleaks)))

    def fake_gitleaks(self, code):
        self.gitleaks.write_text(f'#!/bin/sh\necho "$@" >> "{self.gitleaks}.calls"\nexit {code}\n')
        self.gitleaks.chmod(0o755)

    def commit(self, name, text, message):
        (self.repo / name).write_text(text)
        self.git("add", "-A")
        self.git("commit", "-q", "-m", message)

    def push(self, *refs):
        return self.git("push", "-q", "origin", *refs, check=False)

    def remote_head(self, branch):
        return subprocess.run(["git", "rev-parse", "--verify", "-q", branch], cwd=self.remote,
                              capture_output=True, text=True).stdout.strip()

    def test_private_term_blocks_push_without_printing_it(self):
        before = self.remote_head("main")
        self.commit("notes.md", "zyxample corp\n", "docs: notes")
        result = self.push("main")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("private denylist term", result.stderr + result.stdout)
        self.assertNotIn("zyxample", (result.stderr + result.stdout).lower())
        self.assertEqual(self.remote_head("main"), before)

    def test_gitleaks_finding_blocks_push(self):
        self.commit("notes.md", "clean\n", "docs: notes")
        self.fake_gitleaks(1)
        self.assertNotEqual(self.push("main").returncode, 0)

    def test_missing_gitleaks_fails_closed(self):
        self.commit("notes.md", "clean\n", "docs: notes")
        self.gitleaks.unlink()
        result = self.push("main")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("make tools", result.stderr + result.stdout)

    def test_clean_push_scans_only_outgoing_commits(self):
        public = self.remote_head("main")
        self.commit("notes.md", "clean\n", "docs: notes")
        tip = self.git("rev-parse", "HEAD").stdout.strip()
        result = self.push("main")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.remote_head("main"), tip)
        calls = Path(f"{self.gitleaks}.calls").read_text()
        self.assertIn(f"--log-opts={public}..{tip}", calls)

    def test_new_branch_is_scanned(self):
        self.git("checkout", "-q", "-b", "topic")
        self.commit("notes.md", "Zyxample Corp\n", "docs: notes")
        self.assertNotEqual(self.push("topic").returncode, 0)
        self.assertEqual(self.remote_head("topic"), "")


if __name__ == "__main__":
    unittest.main()
