import json
import os
from pathlib import Path
import sys
import unittest
from types import SimpleNamespace
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parent))
from repclient import Client, RepError, evaluation_value


class ClientTests(unittest.TestCase):
    def test_scope_is_post_verb_and_does_not_inherit_accidentally(self):
        env = {"REP_TASK": "parent", "REP_WORKSPACE": "parent-project", "PATH": "/test"}
        child = Client(environment=env, workspace="fixture", task="child", browser="headless")
        argv = child.argv(["browser", "eval", "1", "--tab", "1"])
        self.assertEqual(argv[1:3], ["browser", "eval"])
        self.assertGreater(argv.index("--task"), 2)
        self.assertNotIn("parent", argv)
        self.assertNotIn("REP_TASK", child.environment)
        self.assertEqual(env["REP_TASK"], "parent")
        self.assertIsNone(Client(environment=env).task)
        self.assertEqual(Client(environment=env, inherit_scope=True).task, "parent")

    def test_literal_values_keep_double_dash_boundary(self):
        argv = Client(task="fixture").argv(["browser", "eval", "--", "-1"])
        self.assertLess(argv.index("--task"), argv.index("--"))
        self.assertEqual(argv[-1], "-1")

    def test_result_and_nonzero_exit_are_checked(self):
        with mock.patch("repclient.subprocess.run", return_value=SimpleNamespace(returncode=0, stdout='{"tab_id":1}')) as run:
            self.assertEqual(Client().call("browser", "tabs"), {"tab_id": 1})
            self.assertFalse(run.call_args.kwargs.get("shell", False))
        with mock.patch("repclient.subprocess.run", return_value=SimpleNamespace(returncode=1, stdout='{}')):
            with self.assertRaises(RepError):
                Client().call("browser", "close", "1")

    def test_malformed_output_is_not_echoed(self):
        with mock.patch("repclient.subprocess.run", return_value=SimpleNamespace(returncode=1, stdout='fake-secret')):
            with self.assertRaises(RepError) as error:
                Client().call("scope")
            self.assertNotIn("fake-secret", str(error.exception))

    def test_errors_preserve_machine_code_and_next(self):
        payload = {"error": {"code": "ambiguous_target", "message": "choose a handle", "next": "rep tabs"}}
        with mock.patch("repclient.subprocess.run", return_value=SimpleNamespace(returncode=1, stdout=json.dumps(payload))):
            with self.assertRaises(RepError) as error:
                Client().call("browser", "tabs")
            self.assertEqual(error.exception.code, "ambiguous_target")
            self.assertEqual(error.exception.next, "rep tabs")

    def test_eval_decode_never_treats_exception_as_success(self):
        self.assertEqual(evaluation_value({"result": {"result": {"value": [1, 2]}}}), [1, 2])
        with self.assertRaises(RepError):
            evaluation_value({"result": {"exceptionDetails": {"text": "synthetic error"}}})


if __name__ == "__main__":
    unittest.main()
