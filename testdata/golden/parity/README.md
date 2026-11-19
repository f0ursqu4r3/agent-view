# Parity goldens

These five files record what the TypeScript implementation gave for the same
inputs as the Go program. They were recorded once, from the TypeScript
collector, hub and audit script at commit `37a1695`. That is the last commit
with the TypeScript code in the tree. Nobody edits them by hand.

`internal/parity` compares the Go side with these files (the `TestGolden*`
tests). The tests need no Bun.

## Files

- `fixtures.json`: the normalized output of `collect --stdio` over the fixture
  logs (`testdata/fixtures` and `testdata/parity`). It holds the ordered
  envelopes, the `source.status` envelopes, the timed-out `tool.end`
  envelopes and the last `coverage` envelope.
- `hub.json`: the normalized `/api/snapshot` body of the hub. The hub had the
  local host and one fake remote host (`fakehost`). The file has the keys
  `hosts`, `ordered`, `statuses` and `timeouts`. It has no `coverage` key: the
  coverage envelope of each host is inside `ordered`, and `statuses` and
  `timeouts` are empty.
- `audit.json`: the report that the TypeScript live-audit script printed. It
  read the `/api/snapshot` of the Go hub, which ran with inputs of the same
  kind as `hub.json`.
- `demo-normal-60000.ndjson`: the events of the TypeScript demo source at
  `normal` scale on a virtual clock, for 60 000 ms. One event on each line.
- `demo-stress-10000.ndjson`: the same at `stress` scale, for 10 000 ms.

## Placeholders

The recording runs used a temporary HOME and the machine's own host name.
The files replace these run-specific values:

- `<HOME>`: the temporary HOME directory, in every string and map key.
- `<LOCAL>`: the label of the hub's local host (the machine's name), alone or
  before `/` in an agent ID.
- `T+<ms>`: an RFC 3339 time in `hub.json`, as milliseconds after `t0` (`T-<ms>`
  for a time before it). `t0` is the base time of the fake logs; the hub
  starts later. The tests do the same to the Go output before they compare.

The normalizer in `internal/parity/normalize_test.go` also drops `seq`, `ts`,
and the `now`, `collectorVersion` and `platform` fields of `hello`.

## How they were produced

The live parity tests ran the TypeScript and the Go program side by side with
Bun 1.4.4. They wrote the files only after the two sides matched, with
`PARITY_WRITE_GOLDEN=1`:

    PARITY_WRITE_GOLDEN=1 go test -count=1 ./internal/parity \
        -run 'TestParity(Fixtures|DemoTrace|HubSnapshot|Audit)'

## How to record them again

The TypeScript code and the live tests are not in the tree any more. To record
the files again:

1. Run `git checkout 37a1695` (or make a worktree at that commit).
2. Install Bun 1.4.4.
3. Run the command above from that checkout.
