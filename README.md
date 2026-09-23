# rep-cli

`rep` is the command-line companion to the [rep+ browser extension](https://github.com/sijan2/rep).
It lets coding agents such as Claude Code and Codex drive your real Arc or Chrome
profile, capture its network traffic with response bodies, and inspect or replay
requests. The CLI reaches the extension through a native-messaging host
(`rep-host`), and captures stay on your machine.

- **Capture**: open pages or run page actions; requests and bodies are archived per task.
- **Inspect**: `summary`, `context`, `list`, `search`, and `body` return compact, filterable views.
- **Act**: replay or mutate captured requests, or run verified UI steps with `browser interact`.
- **Isolate**: each agent works in its own workspace/task, so captured data never mixes.

## Install

Requires macOS, Go 1.25+, and Arc or another Chromium browser.

```sh
git clone https://github.com/sijan2/rep-cli && cd rep-cli
scripts/build_install.sh --host    # runs tests, installs rep and rep-host to ~/.local/bin
```

Make sure `~/.local/bin` is on your `PATH`. Then add the extension and connect it:

```sh
git clone https://github.com/sijan2/rep ~/rep
# arc://extensions or chrome://extensions → Developer mode → Load unpacked → ~/rep
rep browser install --extension-path ~/rep                    # Arc (default)
rep browser install --extension-path ~/rep --browser chrome   # Chrome
# Reload rep+, then check the bridge:
rep browser doctor
```

## Use

Data commands need a workspace and a task; give each agent its own task.
Browser commands use Arc unless you pass `--browser chrome`.

```sh
export REP_WORKSPACE=demo REP_TASK=first-look

rep browser open https://example.com --keep-tab   # navigate in a background tab and capture
rep summary --max-bytes 4096                      # grouped overview with a cursor
rep context --since CURSOR                        # only what changed since that cursor
rep primary example.com                           # mark the target domain
rep list --api                                    # API calls to primary domains
rep body ID --info                                # check completeness, then --head or --pointer
rep replay ID --set 'header.X-Debug=1'            # re-send with a change and diff
```

`rep agent-prompt` prints paste-ready instructions for an agent, and
`rep describe <command>` prints any command's full contract.

Optional:

- `rep browser headless start --extension ~/rep` runs a private headless profile
  per task (needs Chrome for Testing or Chromium). Add `--browser headless` to later commands.
- `rep jev config --env-file /path/to/.env` enables [Jev](docs/jev.md) element lookup
  (`browser select`) and traffic classification. Needs a TypeSafe API key.

## Docs

[Workspaces and context](docs/agent-context.md) ·
[Body capture](docs/body-capture.md) ·
[Interactions](docs/interactions.md) ·
[Headless](docs/headless.md) ·
[Jev](docs/jev.md) ·
[Architecture](docs/architecture.md) ·
[Arc control](docs/arc-browser-control.md) ·
[Android](docs/android-skills.md)

## Develop

```sh
go test ./...
```
