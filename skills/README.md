# Rep skill

The single maintained entry point is [rep/SKILL.md](rep/SKILL.md). Its metadata matches VERSION and its contract is generated from Cobra metadata. Identical repository-local copies live under .agents/skills/rep and .claude/skills/rep. The Go skill test verifies all copies and the command contract; use its explicit update option when metadata changes.

The old rep-cli and rep-browser-network skill entry points are retired. The compatibility download script remains at its existing path for callers while it moves to the final download verb. Installing a skill into a personal agent profile is an owner action, separate from building or testing this repository.
