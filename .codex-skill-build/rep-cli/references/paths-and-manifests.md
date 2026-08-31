# Paths and Native Messaging

## Repo Paths (local default)
- CLI repo: /Users/sijan/bug/rep-cli
- Extension repo: /Users/sijan/bug/rep

## CLI Data Paths
- Store dir: ~/.local/share/rep-cli (or $XDG_DATA_HOME/rep-cli)
- Store file: ~/.local/share/rep-cli/store.json
- Live export: ~/.local/share/rep-cli/live.json
- Override live export: REPLIVE_PATH (supports ~)

## Native Host (rep-host)
- Default install: ~/.local/bin/rep-host
- Native host name: com.repplus.host

## Native Messaging Manifest Paths (macOS)
- Chrome: ~/Library/Application Support/Google/Chrome/NativeMessagingHosts/com.repplus.host.json
- Arc: ~/Library/Application Support/Arc/NativeMessagingHosts/com.repplus.host.json

## Manifest Template
Update the extension ID and rep-host path to match the local install.

```json
{
  "name": "com.repplus.host",
  "description": "rep+ Native Messaging Host for CLI integration",
  "path": "/Users/sijan/.local/bin/rep-host",
  "type": "stdio",
  "allowed_origins": [
    "chrome-extension://<EXTENSION_ID>/"
  ]
}
```

Notes:
- Find the extension ID in arc://extensions or chrome://extensions.
- Reload the extension after updating the manifest.
