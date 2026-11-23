"""Versioned, stdlib-only subprocess boundary shared by rep companions.

The caller owns task selection. Ambient REP_TASK is ignored unless explicitly
requested; subprocess environment changes never mutate the parent environment.
JSON is the default transport; browser and task pins use the child environment.

The governor helpers share rep's per-site cooldown files with the Go CLI, so a
rate limit observed by any client pauses every client for the same site.
"""
import email.utils
import hashlib
import json
import os
import re
import subprocess
import stat
import tempfile
from datetime import datetime, timedelta, timezone
from pathlib import Path


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
        if task:
            self.environment["REP_TASK"] = task if "/" in task or not workspace else workspace + "/" + task
            markers = ("CLAUDE_CODE_SESSION_ID", "CODEX_THREAD_ID", "AI_AGENT", "GEMINI_CLI", "AGENT_SESSION_ID", "OPENAI_SESSION_ID")
            values = [key + "=" + self.environment[key] for key in markers if self.environment.get(key, "").strip()]
            if values:
                self.environment["REP_TASK_SESSION"] = "agent-" + hashlib.sha256("\0".join(values).encode()).hexdigest()[:20]
        if browser:
            self.environment["REP_BROWSER"] = browser

    @property
    def identity(self):
        return [self.binary, self.workspace, self.task]

    def argv(self, arguments):
        arguments = list(arguments)
        if not arguments or arguments[0].startswith("-"):
            raise ValueError("rep client requires a verb first")
        # Identity and browser selection are already pinned in the environment.
        # Preserve every caller argument, including the literal -- boundary.
        return [self.binary, *arguments]

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
    """Decode the version-matched value or its private artifact."""
    if response.get("error"):
        raise RepError(response["error"].get("code", "evaluation_exception"), "page evaluation failed")
    artifact = response.get("meta", {}).get("artifact")
    if artifact:
        path = Path(artifact["path"])
        info = path.lstat()
        if not stat.S_ISREG(info.st_mode) or info.st_mode & 0o777 != 0o600 or info.st_size > 16 * 1024 * 1024:
            raise RepError("invalid_artifact", "eval artifact is not a bounded private file")
        return json.loads(path.read_text())
    if "untrusted" in response:
        return response["untrusted"].get("value")
    raise RepError("invalid_output", "expected the version-matched eval result")


# --- rep governor cooldowns --------------------------------------------------
#
# rep's governor keeps one file per site at <GlobalDataDir>/governor/<site>.json:
#   {"until": RFC3339, "reason": "...", "set_at": RFC3339}
# GlobalDataDir is $XDG_DATA_HOME/rep-cli, else ~/.local/share/rep-cli (see
# internal/scope.GlobalDataDir). The site key is the registrable domain, for
# example "facebook.com". Companions read the file before any browser work and
# write it when a site answers with a rate limit; nobody sleeps and retries.

RFC3339_RE = re.compile(r"^(\d{4})-(\d{2})-(\d{2})[Tt ](\d{2}):(\d{2}):(\d{2})(?:\.(\d{1,9}))?(?:[Zz]|([+-])(\d{2}):(\d{2}))$")
SITE_RE = re.compile(r"^(?!-)[a-z0-9-]{1,63}(?:\.(?!-)[a-z0-9-]{1,63})*$")
MULTI_PART_SUFFIXES = ("co.uk", "org.uk", "ac.uk", "com.au", "co.jp", "co.nz", "com.br", "co.in")
DEFAULT_COOLDOWN_SECONDS = 60
MAX_COOLDOWN = timedelta(hours=24)  # same cap as the Go governor


def global_data_dir(environment=None):
    """rep's GlobalDataDir: $XDG_DATA_HOME/rep-cli, else ~/.local/share/rep-cli."""
    env = os.environ if environment is None else environment
    base = (env.get("XDG_DATA_HOME") or "").strip()
    if base:
        if not os.path.isabs(base):
            raise RepError("invalid_environment", "XDG_DATA_HOME must be an absolute path")
        return Path(base) / "rep-cli"
    home = (env.get("HOME") or "").strip()
    return (Path(home) if home else Path.home()) / ".local" / "share" / "rep-cli"


def registrable_domain(host):
    """The site key rep uses: the last two labels, or three for a few known multi-part suffixes."""
    labels = [label for label in (host or "").strip().lower().strip(".").split(".") if label]
    if len(labels) == 4 and all(label.isdigit() for label in labels):
        return ".".join(labels)  # an IPv4 literal is its own site
    if len(labels) > 2 and ".".join(labels[-2:]) in MULTI_PART_SUFFIXES:
        return ".".join(labels[-3:])
    return ".".join(labels[-2:])


def parse_rfc3339(text):
    """An aware datetime for an RFC 3339 timestamp (Go's RFC3339 and RFC3339Nano), else None."""
    match = RFC3339_RE.fullmatch(text.strip()) if isinstance(text, str) else None
    if not match:
        return None
    year, month, day, hour, minute, second, fraction, sign, offset_hours, offset_minutes = match.groups()
    zone = timezone.utc
    if sign:
        delta = timedelta(hours=int(offset_hours), minutes=int(offset_minutes))
        zone = timezone(-delta if sign == "-" else delta)
    try:
        return datetime(int(year), int(month), int(day), int(hour), int(minute), int(second),
                        int((fraction or "0").ljust(6, "0")[:6]), tzinfo=zone)
    except ValueError:
        return None


def format_rfc3339(moment):
    if moment.tzinfo is None:
        moment = moment.replace(tzinfo=timezone.utc)
    return moment.astimezone(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")


def retry_after_seconds(value, now=None):
    """RFC 9110 Retry-After as whole seconds: delta-seconds or an HTTP-date; None when absent or unusable."""
    text = str(value).strip() if value is not None else ""
    if not text:
        return None
    if re.fullmatch(r"\d{1,9}", text):
        return int(text)
    try:
        moment = email.utils.parsedate_to_datetime(text)
    except (TypeError, ValueError, IndexError):
        return None
    if moment is None:
        return None
    if moment.tzinfo is None:
        moment = moment.replace(tzinfo=timezone.utc)
    return max(0, int((moment - (now or datetime.now(timezone.utc))).total_seconds()))


def governor_cooldown_path(site, data_dir=None):
    if not isinstance(site, str) or not SITE_RE.fullmatch(site):
        raise ValueError("site must be a registrable domain such as facebook.com")
    base = Path(data_dir) if data_dir else global_data_dir()
    return base / "governor" / (site + ".json")


def governor_cooldown(site, data_dir=None, now=None):
    """(until, reason) of an active cooldown for `site`; (None, "") when there is none.

    A missing, unreadable or malformed file is no cooldown. `until` is an aware datetime.
    """
    path = governor_cooldown_path(site, data_dir)
    try:
        value = json.loads(path.read_text())
    except (OSError, ValueError):
        return None, ""
    if not isinstance(value, dict):
        return None, ""
    until = parse_rfc3339(value.get("until"))
    if until is None or until <= (now or datetime.now(timezone.utc)):
        return None, ""
    reason = value.get("reason")
    return until, reason if isinstance(reason, str) else ""


def write_governor_cooldown(site, until, reason, data_dir=None, now=None):
    """Record that `site` must not be contacted before `until`; returns the effective until.

    The file is published atomically with mode 0600 in a 0700 directory. An
    already recorded later cooldown is kept, so a shorter hint never cuts an
    earlier back-off short; a cooldown never exceeds 24 hours.
    """
    path = governor_cooldown_path(site, data_dir)
    now = now or datetime.now(timezone.utc)
    if until.tzinfo is None:
        until = until.replace(tzinfo=timezone.utc)
    until = min(until, now + MAX_COOLDOWN)
    existing, _ = governor_cooldown(site, data_dir, now)
    if existing is not None and existing >= until:
        return existing
    path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    os.chmod(path.parent, 0o700)
    descriptor, temporary = tempfile.mkstemp(prefix="." + path.name + ".", suffix=".tmp", dir=path.parent)
    try:
        with os.fdopen(descriptor, "w") as handle:
            json.dump({"until": format_rfc3339(until), "reason": str(reason or "")[:200], "set_at": format_rfc3339(now)}, handle)
            handle.flush()
            os.fsync(handle.fileno())
        os.chmod(temporary, 0o600)
        os.replace(temporary, path)
    finally:
        if os.path.exists(temporary):
            os.unlink(temporary)
    return until
