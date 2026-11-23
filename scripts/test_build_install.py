"""Exercise the installer without compiling, installing or touching a real profile."""
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest


class BuildInstallTests(unittest.TestCase):
    def test_workspace_invocation_builds_inside_module(self):
        source = Path(__file__).resolve().parent
        with tempfile.TemporaryDirectory(prefix="rep-install-test-") as temporary:
            root = Path(temporary)
            cli, extension, caller = root / "cli", root / "extension", root / "workspace"
            scripts = cli / "scripts"
            scripts.mkdir(parents=True)
            extension.mkdir()
            caller.mkdir()
            for name in ("build_install.sh", "install-extension-path.sh"):
                shutil.copyfile(source / name, scripts / name)
            (cli / "VERSION").write_text("test\n")
            (cli / "go.mod").write_text("module fixture\n")
            (extension / "manifest.json").write_text("{}\n")
            stubs = root / "stubs"
            stubs.mkdir()
            # A Go build requires the caller to be in the module even when the
            # package argument is absolute. Require the same thing here, and
            # emit harmless executable fixtures for the --version smoke check.
            (stubs / "go").write_text("""#!/usr/bin/env python3
import json, os, pathlib, sys
with open(os.environ['INSTALL_TEST_LOG'], 'a') as log:
    log.write(json.dumps({'cwd': os.getcwd(), 'args': sys.argv[1:]}) + '\\n')
if not pathlib.Path('go.mod').is_file():
    sys.exit('cannot find main module')
if sys.argv[1] == 'build':
    output = pathlib.Path(sys.argv[sys.argv.index('-o') + 1])
    output.write_text('#!/bin/sh\\nexit 0\\n')
    output.chmod(0o700)
""")
            (stubs / "node").write_text("#!/bin/sh\nexit 0\n")
            for stub in stubs.iterdir():
                stub.chmod(0o700)
            log = root / "builds.jsonl"
            env = dict(os.environ, PATH=str(stubs) + os.pathsep + os.environ.get("PATH", ""),
                       REP_EXTENSION_PATH=str(extension), XDG_CONFIG_HOME=str(root / "config"),
                       INSTALL_TEST_LOG=str(log))
            env.pop("REP_SKIP_TESTS", None)
            result = subprocess.run(["bash", str(scripts / "build_install.sh"), "--host",
                                     "--install-dir", "relative-bin"], cwd=caller, env=env,
                                    capture_output=True, text=True, timeout=15)
            self.assertEqual(result.returncode, 0, result.stderr)
            calls = [json.loads(line) for line in log.read_text().splitlines()]
            self.assertEqual(len(calls), 3)
            self.assertTrue(all(Path(call["cwd"]) == cli.resolve() for call in calls))
            for binary in ("rep", "rep-host"):
                self.assertTrue((caller / "relative-bin" / binary).is_file())
            self.assertFalse((caller / "relative-bin" / "rep-lab").exists())
            recorded = root / "config" / "rep" / "extension-path"
            self.assertEqual(recorded.read_text().strip(), str(extension.resolve()))
            self.assertEqual(recorded.stat().st_mode & 0o777, 0o600)


if __name__ == "__main__":
    unittest.main()
