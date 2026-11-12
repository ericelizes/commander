# Linear My Progress

`local.toml`, beside `config.toml` in Commander home, accepts either the
existing single `team` string or a list:

```toml
[integrations.linear]
team = ["FOR", "ENGCHA", "ENGINT"]
```

`teams = ["FOR", "ENGCHA", "ENGINT"]` is also accepted. Keys are trimmed,
uppercased and deduplicated. Each team contributes its active cycle's issues
assigned to the authenticated viewer. Issues retain their team and identifier.
The merged Progress page displays each team's cycle name and dates. When
cycles differ, it omits a combined time/pace estimate rather than choosing one
team's dates. Added tickets are judged against their own team's cycle start.

Reads refresh every 30 seconds. An unreachable team keeps its last successful
issues and shows its own last-read notice; other teams still refresh. Automated
unmapped-ticket and status checks use only fresh team results.

For Eric's eventual installation, update his existing `[integrations.linear]`
`team` value to the three-team list above, preserving the workspace and other
settings. This source change does not edit his local settings or install binaries.
