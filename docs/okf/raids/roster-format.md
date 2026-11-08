---
type: Reference
title: Roster file format
description: The tb-raid-roster v1 and v2 batch format, specs and the faction check.
resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/raidwalk/roster.go
tags: [raids, roster, reference]
sources:
  - id: roster
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/raidwalk/roster.go
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T21:55:00+01:00
status: draft
---

# Roster file format

`--hires FILE` takes a `tb-raid-roster` v1 or v2 file. The walk hires batches in order and learns each batch's names by diffing `party` before and after the hire.[^roster]

```json
{"format":"tb-raid-roster","version":2,"level":60,"tier":2,
 "batches":[{"role":"mt","hire":"1 warrior:tank","spec":"protection"},
            {"role":"ot","hire":"1 paladin:tank","spec":"protection"},
            {"role":"rest","hire":"1 priest:healer","spec":"holy"},
            {"role":"rest","hire":"2 hunter","spec":"mm"},
            {"role":"rest","hire":"14d"}]}
```

`hire` is `<n>t`, `<n>h`, `<n>d` or `<n> <class>[:<role>]`; a preset (`onyxia`, `mc`, ...) is refused because it fills the raid in one go and leaves no way to know the MT. Exactly one `mt` batch comes first and hires exactly one tank; at most one `ot` batch (tanks only); one or more `rest` batches. 1 to 40 bots, and at most the plan's raid size (the commander stands outside the group, so every raid seat is a hire). `level` defaults to the plan's band maximum; `tier` (1-3) is optional — absent, the walk takes the committed table's row, and a tier that differs from that row needs `--tier-override`.

Version 2 adds `spec` (a class-spec id) and an optional `count` to a batch. After the batch's hire the walk sends `.bot role <name> <spec>` for every bot it added, waits for the server's `role set` answer, and only then runs the tier refit (`.bot gear raid <commander> tier <N>`), which follows the bot's own spec; each bot logs a `roster: <name> <class> <spec>` line. The batch must name one class (`1 priest:healer`, not `5h`), because the spec is validated against that class before anything is sent, and the spec's role must match the hire's (a `bear` needs `1 druid:tank`). The ids are TortoiseBots' `.bot role` ids: warrior `arms fury protection`, paladin `holy protection retribution`, hunter `bm mm survival`, rogue `assa combat subtlety`, priest `discipline holy shadow hybrid`, shaman `ele enh restoration`, mage `arcane fire frost`, warlock `affliction demonology destruction`, druid `balance boomkin cat bear restoration` (a shaman tank is `.bot role tank`, not a spec id). `count`, when present, must be the batch's size. A version 1 roster takes neither field.

For a hunter batch `spec` also sets the talents, because `.bot role` changes the bot's strategies only and a hunter hired with no spec arrives as the default premade, Survival. The ids are `bm`, `mm` and `survival` (`sv` is accepted for `survival`). Before `.bot role`, the walk sends TortoiseBots' own talent command, `.bot command <name> talents beastmastery|marksmanship|survival`, then a bare `.bot command <name> talents`, and reads the bot's answer (`My current talent spec is: ... (a/b/c)`): the tree with the most points must be the spec's (BM 1st, MM 2nd, Survival 3rd). It logs `roster: <name> hunter <spec> talents a/b/c` when it holds. When the build does not take (no answer, `Found multiple specs`, the points in another tree) it logs `hire: spec <x> not applied to <name> (<reason>)` and the walk carries on with the role and the refit. To give two hunters different specs, hire them in two batches (`1 hunter` with `mm`, `1 hunter` with `bm`); the spec belongs to a whole batch. Other classes get the `.bot role` spec only.

Version 2 also takes `commander_faction` (`"alliance"` or `"horde"`): TortoiseBots' hire fills the commander's own faction only, so a Horde roster cannot be hired from an Alliance commander (no shaman) and the other way round (no paladin). The walk reads the logged-in commander's `status.team` before the first hire and fails at once when it is the other side (`roster: the roster wants a horde commander, but <name> is alliance`). A roster without the field is not checked, as in version 1.

Without `--hires` the roster comes from the plan's `raid {size, tanks, healers, dps}`: `1 warrior:tank`, `<tanks-1>t`, `<healers>h <size-tanks>d`, plus the table's tier. The server's hire cap is 39 bots, so a 40-man roster asks for all 40 and gets 39: the summary notes `hire cap 39` and the walk goes on. A shortfall below the cap is a failed walk (exit 18).
