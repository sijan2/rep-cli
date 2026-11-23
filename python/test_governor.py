"""Governor cooldown files shared with the Go CLI: read, write, and the formats they must accept."""
import json
import os
from datetime import datetime, timedelta, timezone
from pathlib import Path
import sys
import tempfile
import unittest
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parent))
from repclient import (RepError, format_rfc3339, global_data_dir, governor_cooldown, governor_cooldown_path,
                       parse_rfc3339, registrable_domain, retry_after_seconds, write_governor_cooldown)

NOW = datetime(2026, 11, 4, 12, 0, 0, tzinfo=timezone.utc)


class GovernorCooldownTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.xdg = Path(self.temp.name) / "xdg"
        environment = mock.patch.dict(os.environ, {"XDG_DATA_HOME": str(self.xdg)})
        environment.start()
        self.addCleanup(environment.stop)

    def test_data_dir_follows_rep_scope_rules(self):
        self.assertEqual(global_data_dir(), self.xdg / "rep-cli")
        self.assertEqual(global_data_dir({"HOME": "/home/fixture"}), Path("/home/fixture/.local/share/rep-cli"))
        with self.assertRaises(RepError):
            global_data_dir({"XDG_DATA_HOME": "relative/path"})
        self.assertEqual(governor_cooldown_path("facebook.com"), self.xdg / "rep-cli" / "governor" / "facebook.com.json")

    def test_site_key_is_a_registrable_domain(self):
        for host, expected in (("www.facebook.com", "facebook.com"), ("facebook.com", "facebook.com"),
                               ("shop.example.co.uk", "example.co.uk"), ("old.reddit.com", "reddit.com"), ("localhost", "localhost"),
                               ("127.0.0.1", "127.0.0.1"), ("1.2.3.4.5", "4.5")):
            self.assertEqual(registrable_domain(host), expected, host)
        for bad in ("../escape", "Facebook.com", "", "a/b", ".hidden", "site.json\n"):
            with self.assertRaises(ValueError):
                governor_cooldown_path(bad)

    def test_missing_malformed_or_expired_file_is_no_cooldown(self):
        self.assertEqual(governor_cooldown("facebook.com", now=NOW), (None, ""))
        path = governor_cooldown_path("facebook.com")
        path.parent.mkdir(parents=True)
        for content in ("not json", "[]", json.dumps({"until": "soon"}), json.dumps({"until": format_rfc3339(NOW - timedelta(seconds=1))})):
            path.write_text(content)
            self.assertEqual(governor_cooldown("facebook.com", now=NOW), (None, ""), content)

    def test_write_then_read_round_trips_privately_and_atomically(self):
        until = write_governor_cooldown("facebook.com", NOW + timedelta(seconds=90), "fbmp: http_429", now=NOW)
        self.assertEqual(until, NOW + timedelta(seconds=90))
        path = governor_cooldown_path("facebook.com")
        self.assertEqual(path.stat().st_mode & 0o777, 0o600)
        self.assertEqual(path.parent.stat().st_mode & 0o777, 0o700)
        self.assertEqual(json.loads(path.read_text()), {"until": "2026-11-04T12:01:30Z", "reason": "fbmp: http_429", "set_at": "2026-11-04T12:00:00Z"})
        self.assertEqual(governor_cooldown("facebook.com", now=NOW), (NOW + timedelta(seconds=90), "fbmp: http_429"))
        self.assertEqual(governor_cooldown("facebook.com", now=NOW + timedelta(seconds=91)), (None, ""))
        self.assertEqual([entry.name for entry in path.parent.iterdir()], ["facebook.com.json"])

    def test_shorter_cooldown_never_cuts_an_active_one_short(self):
        write_governor_cooldown("facebook.com", NOW + timedelta(seconds=300), "first", now=NOW)
        kept = write_governor_cooldown("facebook.com", NOW + timedelta(seconds=30), "second", now=NOW)
        self.assertEqual(kept, NOW + timedelta(seconds=300))
        self.assertEqual(governor_cooldown("facebook.com", now=NOW)[1], "first")
        extended = write_governor_cooldown("facebook.com", NOW + timedelta(seconds=600), "third", now=NOW)
        self.assertEqual(extended, NOW + timedelta(seconds=600))
        self.assertEqual(governor_cooldown("facebook.com", now=NOW)[1], "third")

    def test_explicit_data_dir_and_naive_until(self):
        other = Path(self.temp.name) / "other"
        write_governor_cooldown("reddit.com", datetime(2026, 11, 4, 12, 5), "naive is UTC", data_dir=other, now=NOW)
        self.assertEqual(governor_cooldown("reddit.com", data_dir=other, now=NOW), (NOW + timedelta(minutes=5), "naive is UTC"))
        self.assertEqual(governor_cooldown("reddit.com", now=NOW), (None, ""))

    def test_cooldown_is_capped_like_the_go_governor(self):
        until = write_governor_cooldown("facebook.com", NOW + timedelta(days=30), "Retry-After: a month", now=NOW)
        self.assertEqual(until, NOW + timedelta(hours=24))
        self.assertEqual(governor_cooldown("facebook.com", now=NOW)[0], NOW + timedelta(hours=24))

    def test_go_timestamp_formats_are_accepted(self):
        for text, expected in (("2026-11-04T12:00:00Z", NOW), ("2026-11-04T12:00:00.123456789Z", NOW.replace(microsecond=123456)),
                               ("2026-11-04T13:00:00+01:00", NOW), ("2026-11-04T07:30:00-04:30", NOW)):
            self.assertEqual(parse_rfc3339(text), expected, text)
        for text in ("2026-11-04 12:00", "2026-13-04T12:00:00Z", 12, None, "2026-11-04T12:00:00"):
            self.assertIsNone(parse_rfc3339(text), text)

    def test_retry_after_delta_and_http_date(self):
        self.assertEqual(retry_after_seconds("120"), 120)
        self.assertEqual(retry_after_seconds(" 7 "), 7)
        self.assertEqual(retry_after_seconds("Wed, 04 Nov 2026 12:02:00 GMT", now=NOW), 120)
        self.assertEqual(retry_after_seconds("Wed, 04 Nov 2026 11:00:00 GMT", now=NOW), 0)
        for value in (None, "", "soon", "-5", "1.5"):
            self.assertIsNone(retry_after_seconds(value), value)


if __name__ == "__main__":
    unittest.main()
