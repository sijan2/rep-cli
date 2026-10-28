# rep-cli

`rep` lets coding agents use your real browser (Dia, Arc, Chrome) through the
[rep+ extension](https://github.com/sijan2/rep): read pages, find controls,
capture network traffic with bodies, and replay requests in parallel.

## Install

```sh
git clone https://github.com/sijan2/rep-cli && cd rep-cli
scripts/build_install.sh --host          # installs rep and rep-host to ~/.local/bin
rep browser install --extension-path ~/rep   # then load ~/rep unpacked in the browser
rep browser doctor
```

## Use

```sh
export REP_WORKSPACE=demo REP_TASK=task-1     # one task per agent

rep browser page https://example.com          # text + exact control refs + page_state + stack + data_requests
rep browser do REF --tab ID --click           # act on a returned ref
rep browser state --tab ID                    # content / loading / login wall / consent / challenge / error / empty
rep browser locate 'the Checkout button' --tab ID        # screenshot + Jev -> DOM node, no action
rep browser capture open URL --keep-tab       # archive the page's own requests and bodies
rep browser capture fanout ID --matrix 'query.q=[a,b,c]'  # replay one request in parallel
rep body ID --saved HASH --pointer /path      # read an archived body
```

`rep describe <command>` prints any command's full contract. `rep agent-prompt`
prints instructions for an agent. Jev (TypeSafe) is optional:
`rep jev config --env-file /path/.env`.

## Develop

```sh
go test ./...
```
