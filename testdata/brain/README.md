# Fixtures for the brain's local end-to-end runs

How to run the daemon against a brain on one machine (env, enrollment token, certificate exchange, who dials whom): [LOCAL-E2E.md](LOCAL-E2E.md).

Samples of what the daemon sends to cyber-brain over the gRPC control plane (`pkg/brainapi/v1`), and two tasks to run against. The
wire files are the **proto-JSON** (protojson, proto field names, only the fields that are set) of the messages, **made by the
daemon's own code** from tests (never written by hand), so they cannot drift from what the daemon really sends. The task documents are
written by hand, and the daemon's tests import and validate them.

Regenerate after a change to the wire or to the task format (a test fails with this command when a file is out of date):

```sh
go test ./internal/useCase/assistant -run TestBrainFixtures -update-brain-fixtures
go test ./internal/useCase/exercise  -run TestBrainFixtures -update-brain-fixtures
```

Generated ids (run, task, version) are replaced by fixed ones (`00000000-0000-4000-8000-0000000000NN`, in order of appearance);
`team_ref` values are random hex in the daemon and fixed here. Nothing here is a secret: the WireGuard config in the StartRun samples is
a stub, and no sample carries a pull credential (`GetOpponent` answers are received, not sent).

## `wire/`: what the daemon sends

| File | Message | What it shows |
|---|---|---|
| `push_data_web_dynamic.json` | `PushDataRequest` | A dynamic task from two events, 5 teams. Every figure measured: `teams_engaged`, `wrong_submissions_per_team` (solvers, before the solve) and `_per_nonsolver`, `lab_health`, `egress_enabled`, `egress_share`, `vpn_bytes_per_team`, `devices_touched_per_team`, `console_minutes_per_team`, and `team_records[]` with a per-event random `team_ref` (the same team has another ref in another event; the same ref across the tasks of one event). Two teams have no activity record: their effort fields are absent. The records are shuffled. |
| `push_data_static_answers.json` | `PushDataRequest` | Two static tasks in one batch: no lab, so no `lab_health`, `egress_enabled` false; the laboratory reports nothing, so `egress_share` and the three effort distributions are `null` (not reported, never zero); the second task nobody solved (no solve-time and no per-solver wrong figures). |
| `push_data_state_checks.json` | `PushDataRequest` | A task with state-checked subtasks: `state_checks[]` with the time from lab-ready to each pass; no `wrong_submissions_per_team` (no answer is submitted). Events refuse state tasks today, so this is the shape the Academy source will send (`source: "academy"`). |
| `lab_facts.json` | `LabFactsRequest` | The lab facts of a grader run, all reported. |
| `lab_facts_not_reported.json` | `LabFactsRequest` | The same call when the laboratory keeps no activity reports (or no device_touch events): the four optional activity fields are unset (not measured). |
| `start_run_grader.json` | `StartRunRequest` | A grader: `access.vpn.entry` of the run's own client, no `addr`, no `band`, no `budget`, no `spec_id`. |
| `start_run_opponent.json` | `StartRunRequest` | The attacker of `tasks/opponent-lab` deployed from a `GetOpponent` spec: `spec_id`, `access.vpn{entry, addr}` (the runner of the tools device on the control gateway; the tunnel reaches exactly that address), `band`, `goal`, `foothold`, `brief`, `budget`. |

Each item of a `PushDataRequest` is one `TaskAggregate{json}`: the backend's `Aggregate` as JSON bytes, which proto-JSON writes as
base64 (`jq -r '.aggregates[0].json | @base64d' testdata/brain/wire/push_data_web_dynamic.json` shows it); the field notes above
are of that JSON. 64-bit integers (`budget.tokens`) are strings in proto-JSON.

Notes: the aggregates are the **cumulative** figures as of `as_of`; a later push for the same `(source, task_ref, version, image)`
replaces the earlier one. A task with several versions or images gives several entries. The `image` is a sha256 fingerprint of the
version's device images (empty for a static task).

## `tasks/`: catalog import format (`exercise.yaml`, `cib-exercise/1.0`)

| Folder | What |
|---|---|
| `web-dynamic/` | A dynamic web task: one `nginx` device behind a switch and a VPN gateway, one flag subtask (`ICE{brain_web_probe}`, delivered to the device by its bootstrap). For the grader. |
| `opponent-lab/` | Two target devices (`web`, `db`) behind a switch `sw1` and a VPN gateway, two subtasks (`recon`, `root`), and an `opponents[]` manifest: an attacker (band 3, foothold `sw1`) and a defender (band 2, starts after `recon`, operates `web` and `db`). Opponents are Academy-only; the platform adds the tools device, its image and the budget. |
| `opponent-lab/opponents.json` | The manifest as the daemon stores it (generated). |
| `opponent-lab/deployed-topology-attack.json` | The lab as it is deployed for the attacker (generated): the document's devices plus the **tools device** cabled on the foothold switch and the hidden **control gateway `ctl`** (with its own switch) that carries only the platform's control of the tools device (second interface of the tools device, fixed address). |
| `opponent-lab/deployed-topology-defense.json` | The same for the defender (the tools device sits on the segment of the first operated device). |

Import a task: zip the folder (`exercise.yaml` at the root of the zip) and use the exercises site "Import", or
`POST /api/exercises/import` with the archive. The import always creates a draft; publish it to run the grader or an opponent
(`.../variants/{variantID}/measure` for the grader, `.../opponents/{index}/try` for an opponent).
