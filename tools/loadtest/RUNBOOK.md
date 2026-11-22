# Load test: 15 teams, one owner at the wheel

A copy lives in `/Volumes/Projects/My/CyberICEBox/docs/loadtest/` (the root folder is not under git; the backend repository's `docs/` folder is in .gitignore, so the file is kept here, next to the tool). Edit both.


Prepared on: 2026-10-20. The owner runs the test by hand. The tools were prepared and checked only on a local stand (a separate Postgres, a daemon started in the `make run-local` style, no cluster). None of this was run against AWS.

The owner's scenario: a separate event, resource reservation starting 2 hours before the start, one team of 2 participants registered by hand by the owner, the other 14 teams (up to 15 participants each) created by the script. Node sizes and the worker calculation: `docs/reviews/loadtest-sizing.md` in the project root.

## What is included

| What | Where | Purpose |
|---|---|---|
| `loadtest-users` | `backend` (renamed from `daemon` on 2026-11-22), branch `feat/loadtest-tools`, `cmd/loadtest-users`; binary `/app/loadtest-users` in the backend image | creates and deletes test accounts (`create`, `delete`) |
| `loadtest` | `backend`, `tools/loadtest` (pure stdlib, builds on the owner's laptop) | participant scenario and report |
| `loadtest-users-job.yaml` | `tools/loadtest/` | Job for `delete` in the cluster. Never applied |
| This file | `tools/loadtest/RUNBOOK.md` | order of actions |

Accounts: `lt-<run>-NNN@loadtest.invalid`, role `user`, confirmed address, random password, one open session per account. The `lt-<run>-` label in the address is the only link to a run: `delete --run <run>` touches only such addresses and never deletes non-`user` roles. The captcha and all security settings are unchanged: the session is issued by the service itself with the same cipher as a normal sign-in. The passwords are in the file for a stand without a captcha (there the script signs in by password itself); in production the captcha is on, so the script takes a ready cookie.

## What was checked and what was not

Checked locally (Postgres 17 in Docker, the daemon from this branch, an event from `cmd/seed`):

- `loadtest-users create/delete` on a real database: the accounts are active, the password works, the cookie opens with the daemon's codec, `GET /api/auth/me` answers 200, `delete` soft-deletes and revokes sessions, a second `delete` does nothing, other people's accounts and another run are intact (integration test `internal/loadtest`).
- `--out -` writes clean JSON to stdout (logs go to stderr).
- The script: registration, joining the event, creating a team, joining by code, closing the roster, waiting for the start (warm-up), the board, scoreboard polling. A unit test with a mocked API runs the whole scenario with the race detector.

Not checked; you will see it for the first time on the stand:

- Dynamic labs (`Ready`, start time, VPN config, link to the web device): there is no lab agent locally. These steps are covered only by an API mock built from the documented fields (`Phase`, `Ready`, `VPNReady`, `Config`).
- The Job and `kubectl exec` in a live cluster.
- The live screen with a real screen link.
- SSE streams (`/results/live`). The real event site receives the scoreboard through SSE and polls every 30 s as a fallback; the script polls `/results` and `/results/changes` every 30 s (as the fallback path). The test does not create the load of open SSE streams (limit `HTTP_INFLIGHT_STREAMS_MAX=200` per replica).

## What the local run found (take into account in advance)

1. **Registration closes at the moment of the start** (`closed_at_start`, `roster_frozen_at_start`). Joining the event and the teams must be finished before the start. The script is therefore split into stages: `prepare` (before the start) and `run` (the test itself).
2. **The maximum team size** in the event settings must be at least 15 (in the seeded event it is 3, so 6 of 12 team joins got 409). The number of teams must be at least 15 (14 from the script + yours).
3. The board (`GET /api/events/{id}/teams/challenges/mine`) answered **500** for a user who is not an event participant. Fixed in this same branch: it is now 403 (`ErrParticipantAccessForbidden`, as for the lab routes). If the board gives 403 during the test, the participant's join of the event failed.
4. The session cookie travels over the network as `url.QueryEscape(value)` (gin decodes it on read). The script does exactly this; if you check with `curl`, encode `+ / =`.

## Preconditions (checklist before starting)

The event (created by the owner, in the platform panel):

- [ ] A separate event (so the statistics of real ones stay clean), its tag = `--event-slug`. Published, entry open, manual approval of participants off.
- [ ] Maximum team size at least 15, number of teams at least 15, start no sooner than the preparation time (see the timeline), duration at least the run duration.
- [ ] Tasks: 6 dynamic ones (all `micro`, see sizing) and static ones from the production catalog. Published on the board, display mode as for production events.
- [ ] Resource reservation for the event, starting 2 hours before the start: `PUT /api/infrastructure/calendar/events/{eventID}/reservation` (through the panel). Check: `GET` of the same path.
- [ ] Your team of 2 participants is registered by hand. If its name is taken by the script: the script names teams `LT Team 01` ... `LT Team 14`.
- [ ] If correct flags are needed: a `flags.json` file `{"<task name or challenge ID>": "<flag>"}`. For dynamic tasks with the flag inside the lab the script sends only wrong flags. Without the file the script also works, there will be no correct answers.
- [ ] For the live screen: a screen link is created in the event panel, its token goes into `--live-token`.
- [ ] Mail: addresses `*@loadtest.invalid` are not delivered. The backend from this branch does not send mail to RFC 2606 reserved domains at all (`.invalid`, `.test`, `.example`, `.localhost`): the delivery target is marked as done with a "skipped: reserved domain (RFC 2606), mail was not sent" note (shown to the user in Ukrainian), with no retries and no error records. Make sure the stand runs exactly such an image, otherwise signals (for example the start reminder) will go to these addresses.

Image and binary:

- [ ] The backend is deployed from an image built with this branch (a second build and a `COPY` were added to the Dockerfile). Check: `kubectl exec -n <NS> deploy/backend -- /app/loadtest-users` prints usage. If the image is old, release first, otherwise there is nowhere to run `create`.
- [ ] `loadtest` is built on the owner's machine: `cd backend && GOWORK=off go build -o /tmp/loadtest ./tools/loadtest` (Go 1.27).

Infrastructure:

- [ ] Laboratory v1.4.0, Cluster Autoscaler installed and checked in a smoke run, the Phase 3 operator (capacity budget) released.
- [ ] CA limited to at most 10 nodes: `LAB_WORKER_MAX=10` (group `<cluster>-lab-worker-*`), the budget `LAB_BUDGET_CPU` and `LAB_BUDGET_MEMORY` set (and equal to the chart's `capacity.budget`). Node type `m7i.xlarge` (`LAB_WORKER_INSTANCE_TYPE`). Check: `make aws asg ARGS=status`.
- [ ] Images are pre-warmed (`PrewarmImages`) for the 6 dynamic tasks, node labels `cybericebox.io/lab=true`, `laboratory.cybericebox.com/node-agent-ready=true` (section 7 of sizing).
- [ ] Replicas: for 210 sessions and 15 x 15 browser streams it is worth enabling `BACKEND_REPLICAS=2` and `EVENT_FRONTEND_REPLICAS=2` or the HPA (`PLATFORM_HPA=true`), in advance (see [infrastructure/docs/event-scaling.md](../../../../infrastructure/docs/event-scaling.md)). Check the database connection budget with the formula from the same document.
- [ ] Cloudflare: the proxy is currently off by default. If it is on, the whole test comes from one address, and attack mode/rules may cut it off; decide in advance. All backend limits are counted per account, not per address (user 1200 per minute, burst 600; flag submission 5 per 30 s per task and 20 per minute per team; 8 concurrent requests per session).
- [ ] Metrics: `metrics-server` answers (`make aws kube ARGS='top pods -n <NS>'`).

Budget (money):

- `m7i.xlarge` per sizing: 0.2016 USD per hour per node. Expected 3 nodes: about 0.60 USD per hour. Upper bound by CA: 10 nodes, about 2.02 USD per hour (without disks and NLB).
- Window: reservation 2 hours before + run about 1 hour + CA scale-down about 15 minutes: roughly 3.5 hours. Expected about 2 USD, ceiling about 7 USD if CA brings up all 10.
- The test does not use Spot nodes (`WORKER_SPOT`); a node interruption would spoil the measurement.

## Timeline (T = event start)

| When | What |
|---|---|
| T-3h | preconditions, check of the image and CA, `LAB_WORKER_*` |
| T-2h | the reservation begins, CA prepares nodes, images are pre-warmed |
| T-75 min | create the accounts (cookies live `SESSION_IDLE_TTL`, 12 hours) |
| T-45 min | stage `prepare`: sign-in, joining, teams |
| T-10 min | stage `run` starts by itself, waits for the start and warms up the pages (`--prewarm`) |
| T | start, participants open the board, labs come up |
| T+60 min | end of the run (`--duration`), report |
| after | cleanup |

If the reservation or the nodes behave unexpectedly before the start, move the start: registration is closed only after T.

## Commands

All paths are relative to the directory `/Volumes/Projects/My/CyberICEBox`. Variables, once:

```bash
NS=<platform_namespace>                       # make aws kube ARGS='get ns'
RUN=lt1                                       # 3-20 characters a-z0-9, new for every test
DOMAIN=<stand domain>                         # the backend's DOMAIN, without api.
SLUG=<tag of the test event>
OUT=/Volumes/Projects/My/CyberICEBox/infrastructure/private/generated/aws/loadtest
KUBECONFIG=/Volumes/Projects/My/CyberICEBox/infrastructure/private/generated/aws/kubeconfig.yaml
export KUBECONFIG
mkdir -p "$OUT" && chmod 700 "$OUT"
```

Do not use `make aws kube` for `create`: `make` prints its own command line to stdout, and it would end up in the file with secrets. A direct `kubectl` with this kubeconfig writes only the process's answer to stdout.

### 1. Create the accounts (14 teams x 15 = 210, plus 3 manual)

```bash
kubectl exec -n "$NS" deploy/backend -- /app/loadtest-users create --run "$RUN" --count 210 --manual 3 --out - > "$OUT/$RUN.json"
chmod 600 "$OUT/$RUN.json"
python3 -c "import json,sys; d=json.load(open('$OUT/$RUN.json')); print(len(d['accounts']), 'accounts,', sum(a.get('manual', False) for a in d['accounts']), 'manual; cookies valid until', d['cookieExpiresAt'])"
```

`--manual` (3 by default) creates that many more accounts, marked `"manual": true`. They are confirmed, with a password but without a session, and the script does not touch them: it does not sign in as them, register them or add them to teams. Details below, in the section "Manual accounts".

The secrets travel through the `kubectl exec` stream to you and do not end up in the pod log or on the pod's disk. If a team has fewer than 15 participants (`--members-min`), the extra accounts stay unused.

If an error occurs in the middle (`account N of 210`): some accounts are already created. Delete the run (`delete --run`, below) and create again with a new `RUN`.

### 2. Plan without load

```bash
/tmp/loadtest --dry-run --domain "$DOMAIN" --event-slug "$SLUG" --creds "$OUT/$RUN.json" --teams 14 --members-min 10 --members 15 --seed 1
```

Prints the teams, sizes and captains. The same `--seed` gives the same layout in both stages.

### 3. Stage prepare (before the start)

```bash
/tmp/loadtest --domain "$DOMAIN" --confirm-domain "$DOMAIN" --event-slug "$SLUG" --creds "$OUT/$RUN.json" \
  --stage prepare --teams 14 --members-min 10 --members 15 --seed 1 --ramp-up 5m --report "$OUT/$RUN-prepare.json" 2>&1 | tee "$OUT/$RUN-prepare.log"
```

`--confirm-domain` is mandatory: the tool loads the stand it points to and does not start without repeating the domain. 0 errors are expected. If the event needs extra team fields: `--team-fields '{"field":"value"}'`.

### 4. Stage run (warm-up, start, play)

```bash
/tmp/loadtest --domain "$DOMAIN" --confirm-domain "$DOMAIN" --event-slug "$SLUG" --creds "$OUT/$RUN.json" \
  --stage run --teams 14 --members-min 10 --members 15 --seed 1 \
  --ramp-up 3m --prewarm 10m --duration 60m --lab-timeout 20m \
  --flags "$OUT/flags.json" --live-token "<screen link token>" --live-viewers 3 \
  --report "$OUT/$RUN-run.json" 2>&1 | tee "$OUT/$RUN-run.log"
```

What a participant does: session check, event information, the board, warm-up (the main page and the scoreboard every 30 s), after the start: scoreboard polling (30 s), the captain watches all the team's labs, everyone waits for `VPNReady` and fetches the VPN config once, then in a loop with 8–45 s pauses refreshes the board, looks at solutions, opens tasks, for dynamic ones checks the lab and takes the link, submits a flag (a correct one with probability `--correct-ratio`, otherwise a wrong one; no more than `--max-wrong` wrong ones per task). The lab start time is counted from the event start (if the run began later, from the run start).

Both stages in one command: `--stage all` (then fit `prepare` into the window before the start). Ctrl-C stops cleanly and prints the report.

The report: a table per endpoint (p50/p95/p99/max, error share, codes), per minute (requests, errors, 429, 5xx, p95), lab start time (p50/p95/max, slowest, how many are not ready), outcome counters. JSON in `--report`.

## Manual accounts (for the owner)

In the file `$OUT/$RUN.json` the last three `accounts` entries have `"manual": true`: these are `lt-<run>-211`, `-212`, `-213` (`@loadtest.invalid`) with the password in the `password` field. With them the owner goes through the whole participant path in the browser: sign-in through ID (the captcha is on, this is a normal sign-in by email and password), registration in the event, creating a team and the rest. The script does not use them in `prepare` or in `run`. Mail is not sent to these addresses (reserved domain), so there will be no confirmation by mail, but the accounts are already confirmed. `delete --run` deletes them together with the others, like all `lt-<run>-*`. To get them: `python3 -c "import json; [print(a['email'], a['password']) for a in json.load(open('$OUT/$RUN.json'))['accounts'] if a.get('manual')]"`.

If the sign-in of these addresses is itself rejected as an invalid domain (the form check on ID), say so: then another marker domain is needed.

## What to watch during the run

Window 1: the script output (a "recent minutes" line every 15 s). The rest in separate terminals from `infrastructure/`:

| What | Command | Norm |
|---|---|---|
| Cluster Autoscaler nodes | `make aws kube ARGS='get nodes -L cybericebox.io/lab,cybericebox.io/asg -w'` and `make aws asg ARGS=status` | nodes appear and become `Ready` within minutes, at most 10 |
| CA decisions | `make aws kube ARGS='-n kube-system get cm cluster-autoscaler-status -o yaml'` and its pod's logs (`get deploy -n kube-system`, find the autoscaler) | no long-lasting `NotTriggerScaleUp`, no hitting the budget |
| Lab groups and withdrawals | `make aws kube ARGS='get labgroups.laboratory.cybericebox.com -A'`, `get labs.laboratory.cybericebox.com -A`; the `Withdrawn` reason in the group status | no withdrawals; a single withdrawal with a retry is acceptable |
| Capacity warnings | `make aws kube ARGS='get tenants.laboratory.cybericebox.com -A -o yaml'`, look for `CapacityShortfall` | absent |
| Pending pods | `make aws kube ARGS='get pods -A --field-selector=status.phase=Pending'` | a peak at the start, then decreasing |
| Throttling and overload | the `429` and `5xx` columns in the script's per-minute table; `make aws kube ARGS='logs -n <NS> deploy/backend --since=5m'` (look for `rate limit`, `overload`, `503`) | 429 and 503 near zero. A 503 with `Retry-After: 1` means hitting `HTTP_INFLIGHT_MAX` or the database pool |
| Error journal | the platform panel, error journal (API `admin/errors`) | no new groups growing |
| API latency | the script's table, per-minute p95; `make aws kube ARGS='top pods -n <NS>'` | `challenges/mine`, `submit`, `results` p95 within 1 s |
| Backend and HPA | `make aws kube ARGS='get hpa,deploy -n <NS>'`, restarts: `get pods -n <NS>` | no restarts, no OOM |
| Database connections | the query from [event-scaling.md](../../../../infrastructure/docs/event-scaling.md) (the section on the connection budget) | below `max_connections` with headroom |

Also watch the VPN in the three demux modes: the owner does this step by hand, with the mode-switching runbook from infra ([infrastructure/docs/vpn-demux-modes.md](../../../../infrastructure/docs/vpn-demux-modes.md)); the script does not bring up a VPN tunnel, it only checks that the config is issued.

## Stop criteria

The script stops by itself if over the last 1-2 minutes (at least 200 requests) the share of errors and 429s is above 50% (`--abort-error-rate`; `0` disables it). The owner decides the rest. Recommended thresholds (a proposal, not an agreement):

1. 5xx above 5% for two minutes in a row, or any restarts/OOM of backend pods.
2. p95 of the board, flag submission or scoreboard above 3 s for three minutes in a row.
3. Database connections above 85% of `max_connections`.
4. Labs in `Failed` above 20%, or more than one group withdrawal (`Withdrawn`), or `CapacityShortfall`.
5. CA did not add a node within 10 minutes with Pending lab pods, or the node count hit the budget while labs are not ready.
6. Spending went beyond the plan: more group nodes than the expected 3 without a reason, or the test runs longer than the window.
7. Unexpected side effects on other tenants/events.

To stop: Ctrl-C in the script window (the report is saved), then cleanup. The test itself does not stop the labs: they live until the event is deleted or the reservation ends.

## Cleanup

The order matters: accounts are not hard-deleted while teams and attempts reference them (deleting the event removes those).

1. Stop the script (Ctrl-C), save the reports and logs from `$OUT`.
2. The owner deletes the test event in the platform panel (teams, participants, attempts and labs go with it). Cancel the reservation if the event is not needed (`DELETE .../reservation`).
3. Delete the accounts. First a dry run (it only counts):
   ```bash
   kubectl exec -n "$NS" deploy/backend -- /app/loadtest-users delete --run "$RUN" --dry-run
   kubectl exec -n "$NS" deploy/backend -- /app/loadtest-users delete --run "$RUN"
   ```
   Or the Job `tools/loadtest/loadtest-users-job.yaml` (fill in `<NS>`, `<IMAGE>`, `<PG>`, `<RUN>`; `args` has `--dry-run` by default, remove it for a real deletion). Result: the accounts are soft-deleted (the address is freed, name and email cleared), sessions revoked. Running it again is safe. The regular cleanup of deleted accounts removes the rest in the database.
4. Files with secrets: `shred -u "$OUT/$RUN.json"` (or `rm -P`) and delete `$RUN-*.log` if they contain addresses.
5. Return the lab worker group:
   - with CA running the nodes leave by themselves after the scale-down delay (by default about 10 minutes idle); check `make aws asg ARGS=status`;
   - if you raised them by hand or CA did not scale down: `make aws asg ARGS='desired=0'` (the script refuses to go down while labs are alive, delete the event/groups first; `--force` only deliberately);
   - return `LAB_WORKER_MAX` and the budget to the usual values, replicas/HPA to the previous ones (`BACKEND_REPLICAS`, `PLATFORM_HPA`), apply the platform.
6. Cleanliness check: `get labgroups,labs -A` is empty, `get nodes` has no extra group nodes, the user list has no `lt-<run>-`, settings such as the maximum team size are restored if they were changed anywhere other than on the test event.

## `loadtest` parameters

| Flag | Default | Meaning |
|---|---|---|
| `--domain`, `--confirm-domain` | none | stand domain; the second must match |
| `--event-slug` | none | event tag (site `<slug>.<domain>`) |
| `--creds` | none | file from `loadtest-users create` |
| `--teams`, `--members`, `--members-min` | 14, 15, equal to `--members` | teams and size (the size is random within the range, depends on `--seed`) |
| `--account-offset` | 0 | skip the file's first accounts |
| `--stage` | all | `prepare`, `run`, `all` |
| `--ramp-up` | 2m | spread out the participants' start |
| `--prewarm` | 0 | minimum wait on the page before the start (otherwise until `StartTime`) |
| `--max-start-wait` | 3h | the script does not wait longer than this before the start |
| `--duration` | 30m | how long to play after the start |
| `--lab-timeout` | 20m | how long to wait for `Ready` |
| `--think-min/--think-max` | 8s/45s | pauses between actions |
| `--lab-poll`, `--results-poll`, `--warm-poll` | 5s, 30s, 30s | polling intervals |
| `--correct-ratio`, `--max-wrong` | 0.35, 3 | share of correct answers (if the flag is known) and the wrong-answer limit |
| `--flags`, `--live-token`, `--live-viewers` | empty, empty, 3 | flags, screen token, viewers |
| `--form-teams` | true | the captain closes the roster |
| `--team-fields` | empty | JSON of extra team fields |
| `--abort-error-rate` | 0.5 | automatic stop |
| `--api-url`, `--api-host`, `--insecure` | none | local stand (`http://127.0.0.1:18080`, Host `api.<domain>`) |
| `--report` | none | JSON report |

Local smoke run (repeat on your own machine): Postgres in Docker, `.env` with `ENV=development`, `DOMAIN=stand.example.com`, `CAPTCHA_PROVIDER=none` and the secrets, `go run ./cmd/seed --start-in 4m --event-tag seedload`, then `loadtest-users create`, `loadtest --api-url http://127.0.0.1:18080`.
