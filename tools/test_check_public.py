"""Private denylist and outgoing-range scans of tools/check-public.py."""
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest

SCRIPT = Path(__file__).resolve().parent / "check-public.py"
# Made-up terms; real private names belong only in the local denylist file.
TERMS = "# local only\nZyxample Corp\nzyx-internal-profile\n"


class Repository:
    def __init__(self, root):
        self.root = Path(root) / "repo"
        (self.root / "tools").mkdir(parents=True)
        shutil.copy(SCRIPT, self.root / "tools" / "check-public.py")
        self.git("init", "-q")
        self.commit("README.md", "clean\n", "docs: start")

    def git(self, *args):
        return subprocess.run(
            ["git", "-c", "user.name=Example", "-c", "user.email=1+example@users.noreply.github.com", *args],
            cwd=self.root, check=True, capture_output=True, text=True).stdout.strip()

    def commit(self, name, text, message):
        path = self.root / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(text)
        self.git("add", "-A")
        self.git("commit", "-q", "-m", message)
        return self.git("rev-parse", "HEAD")

    def check(self, denylist, *args):
        env = dict(os.environ, EASY_SSO_PUBLIC_DENYLIST=str(denylist))
        result = subprocess.run(["python3", "-I", "tools/check-public.py", *args],
                                cwd=self.root, env=env, capture_output=True, text=True)
        return result.returncode, result.stdout + result.stderr


class PrivateDenylistTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.repo = Repository(self.tmp.name)
        self.denylist = Path(self.tmp.name) / "public-denylist"
        self.denylist.write_text(TERMS)

    def tearDown(self):
        self.tmp.cleanup()

    def assertWithheld(self, output):
        for term in ("zyxample", "zyx-internal-profile"):
            self.assertNotIn(term, output.lower())

    def test_term_in_file_fails_without_printing_it(self):
        self.repo.commit("notes.md", "Owned by ZYXAMPLE corp\n", "docs: notes")
        code, output = self.repo.check(self.denylist)
        self.assertEqual(code, 1, output)
        self.assertIn("private denylist term", output)
        self.assertWithheld(output)

    def test_term_in_file_name_withholds_the_path(self):
        self.repo.commit("zyx-internal-profile.ini", "clean\n", "test: fixture")
        code, output = self.repo.check(self.denylist)
        self.assertEqual(code, 1, output)
        self.assertIn("path withheld", output)
        self.assertWithheld(output)

    def test_denylist_inside_repository_is_refused(self):
        inside = self.repo.root / "public-denylist"
        inside.write_text(TERMS)
        code, output = self.repo.check(inside)
        self.assertEqual(code, 2, output)
        self.assertIn("outside the repository", output)
        self.assertWithheld(output)

    def test_missing_denylist_is_reported_not_fatal(self):
        code, output = self.repo.check(Path(self.tmp.name) / "absent")
        self.assertEqual(code, 0, output)
        self.assertIn("private denylist not configured", output)

    def test_revs_scan_only_outgoing_commits(self):
        public = self.repo.git("rev-parse", "HEAD")
        self.repo.commit("notes.md", "Zyxample Corp\n", "docs: notes")
        leaked = self.repo.git("rev-parse", "HEAD")
        self.repo.commit("notes.md", "clean\n", "docs: tidy")
        clean = self.repo.git("rev-parse", "HEAD")
        code, output = self.repo.check(self.denylist, "--revs", f"{leaked}..{clean}")
        self.assertEqual(code, 0, output)
        code, output = self.repo.check(self.denylist, "--revs", f"{public}..{clean}")
        self.assertEqual(code, 1, output)
        self.assertWithheld(output)

    def test_revs_scan_commit_messages(self):
        public = self.repo.git("rev-parse", "HEAD")
        tip = self.repo.commit("other.md", "clean\n", "fix: for zyx-internal-profile")
        code, output = self.repo.check(self.denylist, "--revs", f"{public}..{tip}")
        self.assertEqual(code, 1, output)
        self.assertIn("commit metadata", output)
        self.assertWithheld(output)


if __name__ == "__main__":
    unittest.main()
