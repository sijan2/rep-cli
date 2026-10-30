import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest


class VerifierDispositionTests(unittest.TestCase):
    def test_retired_entrypoints_fail_before_imports_or_browser_dispatch(self):
        root = Path(__file__).resolve().parent / "verify"
        manifest = json.loads((root / "dispositions.json").read_text())
        rows = manifest["scripts"]
        self.assertEqual(len(rows), 9)
        self.assertEqual(len({row["file"] for row in rows}), 9)
        for row in rows:
            self.assertRegex(row["source_commit"], r"^[0-9a-f]{40}$")
            if row["disposition"] != "retire":
                self.assertEqual(row["disposition"], "port")
                continue
            with self.subTest(script=row["file"]), tempfile.TemporaryDirectory() as directory:
                # No PATH or browser environment: even optional legacy imports
                # must not run before the retirement notice.
                result = subprocess.run([sys.executable, str(root / row["file"])],
                                        cwd=directory, env={}, capture_output=True, text=True, timeout=5)
                self.assertEqual(result.returncode, 1)
                self.assertEqual(result.stdout, "")
                self.assertIn("Retired verifier:", result.stderr)
                self.assertIn(row["source_commit"], result.stderr)
                self.assertNotIn("Traceback", result.stderr)


if __name__ == "__main__":
    unittest.main()
