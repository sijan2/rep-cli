"""Versioned, stdlib-only subprocess boundary shared by rep companions.

The caller owns task selection. Ambient REP_TASK is ignored unless explicitly
requested; subprocess environment changes never mutate the parent environment.
The legacy transport flags are confined here until migration step 9.
"""
import json
import os
import subprocess


class RepError(RuntimeError):
    def __init__(self, code, message, *, next_command=None, returncode=1):
        super().__init__("rep {}: {}".format(code, message))
        self.code = code
        self.next = next_command
        self.returncode = returncode


class Client:
    def __init__(self, *, binary=None, workspace=None, task=None, browser=None,
                 inherit_scope=False, environment=None):
        self.binary = binary or os.environ.get("REP_BINARY", "rep")
        self.environment = dict(os.environ if environment is None else environment)
        if inherit_scope:
            workspace = workspace or self.environment.get("REP_WORKSPACE")
            task = task or self.environment.get("REP_TASK")
        self.workspace, self.task, self.browser = workspace, task, browser
        for key in ("REP_WORKSPACE", "REP_TASK", "REP_TASK_SESSION"):
            self.environment.pop(key, None)
        self.environment["NO_COLOR"] = "1"

    @property
    def identity(self):
        return [self.binary, self.workspace, self.task]

    def argv(self, arguments):
        arguments = list(arguments)
        if not arguments or arguments[0].startswith("-"):
            raise ValueError("rep client requires a verb first")
        # Append pins after the verb but before --, where values become literal.
        split = arguments.index("--") if "--" in arguments else len(arguments)
        flags = ["--raw-json"]
        if self.workspace:
            flags += ["--workspace", self.workspace]
        if self.task:
            flags += ["--task", self.task]
        if arguments[0] == "browser" and self.browser and "--browser" not in arguments:
            flags += ["--browser", self.browser]
        return [self.binary, *arguments[:split], *flags, *arguments[split:]]

    def call(self, *arguments, timeout=60):
        try:
            done = subprocess.run(self.argv(arguments), capture_output=True, text=True,
                                  timeout=timeout, env=self.environment)
        except FileNotFoundError as error:
            raise RepError("binary_missing", "rep is not on PATH") from error
        except subprocess.TimeoutExpired as error:
            # Never retry: a mutation may already have reached the browser.
            raise RepError("outcome_unknown", "command timed out; verify before retrying") from error
        try:
            value = json.loads(done.stdout)
        except (ValueError, TypeError) as error:
            # stdout/stderr can contain secrets; do not echo an unparsed stream.
            raise RepError("invalid_output", "expected one JSON result", returncode=done.returncode or 1) from error
        if isinstance(value, dict) and value.get("error"):
            problem = value["error"]
            if not isinstance(problem, dict):
                raise RepError("command_failed", "malformed error result", returncode=done.returncode or 1)
            raise RepError(problem.get("code", "command_failed"), problem.get("message", "command failed"),
                           next_command=problem.get("next"), returncode=done.returncode or 1)
        if done.returncode:
            raise RepError("command_failed", "command exited without an error object", returncode=done.returncode)
        if not isinstance(value, (dict, list)):
            raise RepError("invalid_output", "expected a result object")
        return value


def evaluation_value(response):
    """Decode the legacy result until eval's untrusted-value contract lands."""
    result = response.get("result", {})
    if result.get("exceptionDetails"):
        raise RepError("evaluation_exception", "page evaluation threw an exception")
    return result.get("result", {}).get("value")
