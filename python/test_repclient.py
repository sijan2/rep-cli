import json
import os
from pathlib import Path
import sys
import tempfile
import unittest
from types import SimpleNamespace
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parent))
from repclient import Client, RepError, evaluation_value


class ClientTests(unittest.TestCase):
    def test_scope_is_bound_in_child_environment_and_does_not_inherit_accidentally(self):
        env = {"REP_TASK": "parent", "REP_WORKSPACE": "parent-project", "PATH": "/test"}
        child = Client(environment=env, workspace="fixture", task="child", browser="headless")
        argv = child.argv(["eval", "1", "t1"])
        self.assertEqual(argv, [child.binary, "eval", "1", "t1"])
        self.assertEqual(child.environment["REP_BROWSER"], "headless")
        self.assertNotIn("--task", argv)
        self.assertNotIn("parent", argv)
        self.assertEqual(child.environment["REP_TASK"], "fixture/child")
        self.assertEqual(env["REP_TASK"], "parent")
        self.assertIsNone(Client(environment=env).task)
        self.assertEqual(Client(environment=env, inherit_scope=True).task, "parent")

    def test_literal_values_keep_double_dash_boundary(self):
        client = Client(binary="rep", task="fixture", browser="headless")
        self.assertEqual(client.argv(["eval", "--", "-1"]), ["rep", "eval", "--", "-1"])
        self.assertEqual(client.argv(["browser", "start"]), ["rep", "browser", "start"])

    def test_result_and_nonzero_exit_are_checked(self):
        with mock.patch("repclient.subprocess.run", return_value=SimpleNamespace(returncode=0, stdout='{"tabs":[]}')) as run:
            self.assertEqual(Client().call("tabs"), {"tabs": []})
            self.assertFalse(run.call_args.kwargs.get("shell", False))
        with mock.patch("repclient.subprocess.run", return_value=SimpleNamespace(returncode=1, stdout='{}')):
            with self.assertRaises(RepError):
                Client().call("close", "t1")

    def test_malformed_output_is_not_echoed(self):
        with mock.patch("repclient.subprocess.run", return_value=SimpleNamespace(returncode=1, stdout='fake-secret')):
            with self.assertRaises(RepError) as error:
                Client().call("scope")
            self.assertNotIn("fake-secret", str(error.exception))

    def test_errors_preserve_machine_code_and_next(self):
        payload = {"error": {"code": "ambiguous_target", "message": "choose a handle", "next": "rep tabs"}}
        with mock.patch("repclient.subprocess.run", return_value=SimpleNamespace(returncode=1, stdout=json.dumps(payload))):
            with self.assertRaises(RepError) as error:
                Client().call("tabs")
            self.assertEqual(error.exception.code, "ambiguous_target")
            self.assertEqual(error.exception.next, "rep tabs")

    def test_eval_decode_never_treats_exception_as_success(self):
        with self.assertRaises(RepError) as error:
            evaluation_value({"error": {"code": "evaluation_exception"}, "untrusted": {"exception": "synthetic error"}})
        self.assertEqual(error.exception.code, "evaluation_exception")
        with self.assertRaises(RepError) as error:
            evaluation_value({"result": {"result": {"value": [1, 2]}}})
        self.assertEqual(error.exception.code, "invalid_output")

    def test_new_eval_value_and_private_artifact(self):
        self.assertEqual(evaluation_value({"untrusted": {"value": 42}}), 42)
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "value.json"
            path.write_text('[1,2]')
            path.chmod(0o600)
            response = {"meta": {"artifact": {"path": str(path)}}}
            self.assertEqual(evaluation_value(response), [1,2])
            path.chmod(0o644)
            with self.assertRaises(RepError):
                evaluation_value(response)
        with self.assertRaises(RepError):
            evaluation_value({"error": {"code":"evaluation_exception"}, "untrusted":{"exception":{}}})


if __name__ == "__main__":
    unittest.main()
