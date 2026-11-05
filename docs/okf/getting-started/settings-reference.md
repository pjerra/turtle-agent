---
type: Reference
title: Settings file
description: Every settings file key with its default, validation, and an example.
resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/config/config.go
tags: [getting-started, reference, settings]
sources:
  - id: config
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/config/config.go
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T21:25:00+01:00
status: draft
---

# Settings file

JSON file, mode `0600`, default `<home>/.config/turtle-agent/settings.json`. Unknown keys are refused. A leading `~/` in folder paths is the home folder.

```json
{
  "realmd": "<server>:3724",
  "realm": "<realm>",
  "world_addr": "<server>:8085",
  "rpc": "127.0.0.1:8765",
  "rpc_auth": "on",
  "rpc_token": "<16-or-more-visible-characters>",
  "accounts": { "<account>": "<password>" },
  "bots": ["<character>"],
  "test_spell": 52524,
  "spot": { "map": 1, "x": -1287.0, "y": -2379.6, "z": 91.7 },
  "mmaps_dir": "<mmaps-dir>",
  "tbm_addon_dir": "<addon-dir>",
  "dungeons_dir": "<dungeons-dir>",
  "rotations_dir": "<rotations-dir>",
  "traces_dir": "<traces-dir>",
  "tb_dashboard_url": "<dashboard-url>",
  "addons": ["Blizzard_BindingUI"],
  "server_name": "<server>",
  "state_dir": "<state-dir>",
  "shared_fingerprint": false,
 - `realm` (optional): realm to pick (case-insensitive) when the list has several. Default: the first realm. A named realm that is offline or missing refuses the login.
}
```

- `realmd` (required): `host:port` of the login server (RealmServerPort). Must be `host:port`.
- `accounts` (required): at least one account name to password. Empty names or passwords are refused. Lookup is case-insensitive (realmd upper-cases names).
- `realm` (optional): realm to pick (case-insensitive) when the list has several. Default: the first realm. A named realm that is offline or missing refuses the login.
- `world_addr` (optional): override the world address the realm list returns. Must be `host:port`.
- `rpc` (optional): RPC listen address. Default `127.0.0.1:8765`. Must be a loopback address.
- `rpc_auth` (optional): `"on"` (default) or `"off"`. Off leaves the RPC open to every local process.
- `rpc_token` (optional): shared bearer token, at least 16 visible ASCII characters, no spaces. Set together with `"rpc_auth": "off"` it is refused.
- `bots`, `spot`, `test_spell` (optional): hints for scripts: bot names, one map-and-coordinates spot for live checks, one spell id. Defaults: none.
- `mmaps_dir` (optional): folder of the server's `data/mmaps` files for `path_to` and `walk_path`. Must be a folder when set.
- `tbm_addon_dir` (optional): TortoiseBotsManager checkout the addon host runs. Default: the pinned copy under the working directory.
- `dungeons_dir` (optional): folder `dungeon_run` reads plans from. Default `dungeons` under the working directory.
- `rotations_dir` (optional): folder of extra rotation files; a bad file stops the login.
- `traces_dir` (optional): folder run traces go to. Default `traces` under the working directory.
- `tb_dashboard_url` (optional): dashboard URL a raid run links its card to (`Raid view: <url>/#commanders`). Absent: no link.
- `addons` (optional): addon list sent at world login. Default: the stock 1.12.1 client's `Blizzard_*` set. Must contain the four fingerprint addons.
- `server_name` (optional): slots-config name of this slot's server (slot setup writes it). Alone it never authorizes GM kills.
- `state_dir` (optional): where the addon fingerprint and the instance-entry ledger are kept. Default: the settings file's folder. Must be an existing folder only you can write.
- `shared_fingerprint` (optional): one addon fingerprint file for all accounts. Default: off (one file per account, because the core's ban hash mixes the fingerprint with the IP).
- `run_meta` (optional): default AI driver of the slot's runs (`model`, `effort`, `run` label, `server_trace`): `dungeon_run`'s `run_meta` wins when it names one. Each of model/effort/run holds at most 64 letters, digits, `.` `_` `-` `+`, and the joined `driver=<model>:<effort>:<run>` holds at most 96 characters; `server_trace` needs a driver to send.
