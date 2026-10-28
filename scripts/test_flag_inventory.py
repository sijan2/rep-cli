#!/usr/bin/env python3
"""Regression checks for the measured help-walker defect."""

import unittest

from flag_inventory import parse_help


class FlagInventoryTests(unittest.TestCase):
    def test_long_help_fields_are_not_subcommands(self):
        children, local, inherited = parse_help("""Compute statistics.

Fields:
  method                  HTTP method
  status                  Response status code
  domain                  Request domain

Usage:
  rep stats [flags]

Flags:
      --count-by string    Count requests by field
  -h, --help               help for stats

Global Flags:
  -j, --json               Output JSON
""")
        self.assertEqual(children, [])
        self.assertEqual(local, ["count-by", "help"])
        self.assertEqual(inherited, ["json"])

    def test_generated_command_groups_are_walked(self):
        children, local, inherited = parse_help("""Usage:
  rep [command]

Browser automation:
  browser      Control a browser

Captured traffic:
  group        Group traffic
  stats        Aggregate traffic
  distribution Show percentages

Flags:
  -h, --help               help for rep

Use "rep [command] --help" for more information.
""")
        self.assertEqual(children, ["browser", "group", "stats", "distribution"])
        self.assertEqual(local, ["help"])
        self.assertEqual(inherited, [])

    def test_long_help_group_options_are_not_subcommands(self):
        children, _, _ = parse_help("""Show options:
  count                     Request count per group
  statuses                  Status code breakdown

Examples:
  rep group --by method

Usage:
  rep group <field> [flags]

Flags:
      --show string        What to show
""")
        self.assertEqual(children, [])


if __name__ == "__main__":
    unittest.main()
