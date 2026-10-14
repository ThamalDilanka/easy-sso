"""Keep the downloadable demo self-contained and incapable of external loading."""
from html.parser import HTMLParser
from pathlib import Path
import unittest


DEMO = Path(__file__).resolve().parents[1] / "docs/demo/easy-sso-demo.html"


class Document(HTMLParser):
    def __init__(self, source):
        super().__init__()
        self.tags = []
        self.feed(source)

    def handle_starttag(self, tag, attrs):
        self.tags.append((tag, dict(attrs)))


class OfflineDemoTests(unittest.TestCase):
    def test_resources_are_embedded(self):
        document = Document(DEMO.read_text())
        for tag, attrs in document.tags:
            self.assertNotIn(tag, {"iframe", "object", "embed", "base", "form"})
            for name in ("src", "href", "srcset", "action", "poster"):
                if name in attrs:
                    self.assertTrue(attrs[name].startswith("data:"), (tag, attrs))

    def test_network_blocking_policy_precedes_code(self):
        document = Document(DEMO.read_text())
        policy = next((i, attrs["content"]) for i, (tag, attrs) in
                      enumerate(document.tags) if tag == "meta" and
                      attrs.get("http-equiv", "").lower() == "content-security-policy")
        directives = {part.strip().split()[0]: part.strip().split()[1:]
                      for part in policy[1].split(";") if part.strip()}
        for name in ("default-src", "connect-src", "form-action", "base-uri", "object-src"):
            self.assertEqual(directives[name], ["'none'"])
        self.assertEqual(directives["script-src"], ["'unsafe-inline'"])
        self.assertEqual(directives["style-src"], ["'unsafe-inline'"])
        first_code = next(i for i, (tag, _) in enumerate(document.tags)
                          if tag in {"style", "script"})
        self.assertLess(policy[0], first_code)


if __name__ == "__main__":
    unittest.main()
