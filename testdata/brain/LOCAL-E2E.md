# Running the daemon against cyber-brain locally

A recipe for an end-to-end run on one machine: the daemon (host process), PostgreSQL and a laboratory from the local stand, and a
brain process. The daemon speaks two transports to the brain: **HTTP/SSE** for the authoring chat (`/v1/assistant/*`) and the
enrollment and renewal (`POST /v1/enroll`, `POST /v1/renew`), and **gRPC** for the machine control plane, whose contract is
[`pkg/brainapi/v1/brain.proto`](../../pkg/brainapi/v1/brain.proto) (read its comments: error model, idempotency, deadlines,
keepalive, sizes, stream semantics). The samples are in [`testdata/brain`](README.md). What is **not** verified here is marked.

## Who dials whom

```
 browser --cookie--> daemon --mTLS HTTP/SSE--> brain :8099  chat, enroll, renew
                        |   \----mTLS gRPC-----> brain :9099  runs, outbox, statistics, archives, opponents
                        |                        |            (the daemon is ALWAYS the client of the brain)
                        | gRPC mTLS              | WireGuard (the run's own client, per run)
                        v                        v
                    laboratory agent -----> lab network <-- each opponent run reaches the runner of ITS tools device at ONE address (a /32 on `ctl`)
```

- **The daemon dials the brain**, never the reverse. Over HTTP: the enrollment, the renewal and the chat (with its event
  stream). Over gRPC, with the same client certificate and the same pinned brain CA: `UploadMaterials`, `StartRun`, `RunEvents`,
  `PostVerdict`, `PostLabFacts`, `StopRun`, `GetOpponent`, `PushData`, `UploadTaskArchive` and the outbox, one bidirectional
  `Requests` stream (`provide_lab`, `provide_device`, `provide_data`, `provide_task` come down, the answers go up). The brain
  must be reachable from the daemon on both ports; it never calls the daemon.
- **The brain dials the lab**: `access.vpn.entry` of StartRun is the full WireGuard config of a client the daemon made for that run
  (its own key, revoked at teardown). The brain brings the tunnel up and speaks to the lab as a participant. For an opponent,
  `access.vpn.addr` is `ip:port` of the runner agent on the run's own tools device, on the hidden control gateway `ctl`; the run's
  tunnel is restricted to exactly that one address. Several opponents in one lab share `ctl`: the tools device of index i has the
  host `.10+i` of its /24, and each run's client reaches only its own device's address. The runner authenticates the brain's per-run credential (`EDA_RUN_ID` and `EDA_TRUST_CA`
  come in the `deploy.env` of the brain's `GetOpponent` spec; with the fallback image the daemon sets them). So the machine that runs the brain must reach the **laboratory's VPN endpoint** (the
  UDP address the agent publishes), and WireGuard must be usable there (root or CAP_NET_ADMIN; a rootless run is not verified).
- The daemon talks to the laboratory agent over gRPC mTLS as for any lab; the brain never touches infrastructure.

## 1. Prerequisites

1. The local stand of the infrastructure repository (`make host-up` in `infrastructure/local`): PostgreSQL, the object store, a
   cluster with the laboratory agent. The backend README ("Running locally") lists what the environment file needs; a working lab
   agent is needed for anything that runs a lab (grader, opponents, tools device).
2. `PLATFORM_SECRETS_KEY` in the daemon's environment file: 64 hex characters. **Without it the assistant stays off** even with an
   endpoint (the stored client key is sealed with it).
3. `ENV=development` in the same file (migrations are read from the source tree).

Never put a secret on a command line or paste it into a chat; write it to the git-ignored files named below.

## 2. Start the brain with an enrollment token

The brain runs its own CA and issues the daemon's client certificate in exchange for a **one-time token**. The token is any random
secret you choose; both sides read it from the variable `BRAIN_ENROLLMENT_TOKEN`, so give it to each process from a file, not by hand:

```sh
# once: a random token in a file outside the repositories
umask 077; head -c 24 /dev/urandom | base64 > ~/brain-enroll.token
```

Start the brain (in `cyber-brain`; flags as in `cmd/brain/main.go`) with the token in its environment, a CA output file for the
daemon, and a SAN for the host name the daemon will use:

```sh
go build -o bin/brain ./cmd/brain
BRAIN_ENROLLMENT_TOKEN="$(cat ~/brain-enroll.token)" \
  ./bin/brain -addr 127.0.0.1:8099 -enroll-tenant daemon -tls-san localhost,127.0.0.1 -ca-out /tmp/brain-ca.crt
```

- Setting the token switches the brain to **mTLS mode**: it serves HTTPS, verifies the client certificate on every route except
  `POST /v1/enroll`, and issues the daemon a certificate whose **CN is the tenant** (`-enroll-tenant`, default `daemon`).
- `-ca-out` writes the brain's CA certificate (never the key): the daemon's `BRAIN_CA_FILE` for the **first** enrollment.
- The inference seats (`INFERENCE_*`, `CYBER_SEAT_*`) are the brain's own configuration; see its README. The authoring, measuring
  and generation seats spend inference.

## 3. Daemon environment

Add to the daemon's environment file (names and defaults are in `internal/config/config.go`, `BrainConfig`):

| Variable | Value |
|---|---|
| `BRAIN_ENDPOINT` | `localhost:8099` (host:port of the brain's HTTPS endpoint: chat, enrollment, renewal; empty switches the assistant off) |
| `BRAIN_GRPC_ENDPOINT` | `localhost:9099` (host:port of the brain's gRPC control plane; stored with the connection; empty: the chat works, runs, outbox and statistics are off) |
| `BRAIN_ENROLLMENT_TOKEN` | the token above (read once; ignored after the first enrollment) |
| `BRAIN_CA_FILE` | `/tmp/brain-ca.crt` (bootstrap trust for the first enrollment; empty: the system roots) |
| `BRAIN_TOOLS_IMAGE` | **deprecated**: the fallback tools image, used only while the brain answers `GetOpponent` with `Unimplemented` (and for a `provide_device` tools lab). No default |
| `BRAIN_RUN_GRACE` | `60s` (how long an unreachable brain is waited for before a run fails) |
| `BRAIN_RUN_MAX_DURATION` | `30m` (the longest a run may last, its lab start included) |
| `BRAIN_OPPONENT_ACTIONS`, `BRAIN_OPPONENT_TOKENS`, `BRAIN_OPPONENT_SECONDS` | `100`, `500000`, `1200` (the budget every opponent run gets; the author never sets one) |
| `BRAIN_CONSOLE_MAX_LIFETIME` | `8h` (caps a console session that never reported its end, in the statistics) |

Then, from the backend repository:

```sh
mise run run-local     # loads the environment file, then go run ./cmd/daemon
```

## 4. The certificate exchange

At start-up the daemon enrolls **once** when no connection for `BRAIN_ENDPOINT` is stored:

1. It makes a key and a CSR and calls `POST /v1/enroll {token, csr_pem}` over **server TLS only** (trusting `BRAIN_CA_FILE`).
2. The brain consumes the token (one use) and answers `{cert_pem, ca_pem}`; the certificate's CN is the tenant. `ca_pem` is the one
   trust anchor both ways: the daemon pins it for every later call (mTLS) and the brain verifies the daemon's client certificate with it.
3. The daemon checks the certificate against its key and the tenant, and stores it in `brain_connections`; the private key is stored
   **sealed** with `PLATFORM_SECRETS_KEY`.
4. It renews the certificate over mTLS (`POST /v1/renew`) once two thirds of its lifetime have passed (the leader replica does it).

The log says whether it enrolled. To enroll again (a new token, a new brain CA): delete the row of the endpoint from
`brain_connections` and restart, or use another `BRAIN_ENDPOINT`.

## 5. Check the connection

- The assistant routes answer `503` until enrolled; `GET /api/assistant/ideas` (signed in as an author) answers once it works.
- In the brain's log: one `POST /v1/enroll`, then mTLS requests; the gRPC `Requests` stream stays open (the daemon's outbox; the
  leader replica only). The brain's first frame on it is a `StreamHello{max_inflight}`: the daemon handles at most that many
  requests at once, answers by `request_id` on whichever stream is open, and after a reconnect handles again what the brain re-sends
  (there is no cursor; a request re-sent while it is being handled is handled once).

## 6. Statistics (PushData)

Collection is **off** until a platform administrator turns it on, per source and per event:

```
PUT /api/admin/assistant/statistics/defaults/events        {"Mode": "continuous"}    # off | final | continuous
PUT /api/admin/assistant/statistics/events/{eventID}       {"Mode": "default"}       # default follows the platform
```

`final` reports an event once it has ended, `continuous` while it runs. The leader pushes every hour (`PushData`: one backend
`Aggregate` JSON object per `TaskAggregate.json`, batches split by size under 16 MiB, rejected items logged); the brain can also ask for
fresh figures with a `provide_data` request in its outbox (answered at once with `data_json`). The effort figures need the laboratory's
activity reports (`activityReports.enabled` in the laboratory chart); without them they are `null` (in `PostLabFacts`: unset). Before enabling this on a real
installation the Privacy Policy wording gets its legal review.

## 7. Grader and opponent runs

1. Import `testdata/brain/tasks/web-dynamic` (zip the folder with `exercise.yaml` at its root; `POST /api/exercises/import` or the
   exercises site) and publish it. The grader needs a working lab (an image the cluster can pull, here `nginx:1.27`).
2. Start a grader run on its variant (`POST /api/exercises/{id}/versions/{versionID}/variants/{variantID}/measure`). The daemon
   deploys a test lab, makes the run's own VPN client, streams the materials (`UploadMaterials`: the meta with `task.json` inline,
   the total size and the sha256, then the files in chunks of at most 1 MiB), then `StartRun`. It follows `RunEvents` (reopened
   with `after_seq` = the last stored event; a closed stream is not the end of the run, only `run_finished` is; `NotFound` ends
   the run as `brain_failed`), judges each `answer` event with the platform checker and replies with `PostVerdict` keyed by
   `(run_id, answer_seq)` (only `correct`, never the expected value); a state subtask that the lab's check decides goes as
   `state=true` with `answer_seq` unset, keyed by `(run_id, subtask_ref)`. `run_finished` `done` finishes the run, `stopped`
   (StopRun or a brain shutdown) ends it stopped, not failed, and `failed` fails it; at the end it sends `PostLabFacts` and destroys the lab.
3. For an opponent import and publish `testdata/brain/tasks/opponent-lab` and try an opponent
   (`POST /api/exercises/{id}/versions/{versionID}/variants/{variantID}/opponents/{index}/try`). The daemon asks the brain
   `GetOpponent(run_id, role, band, task_ref, version)` and deploys the tools device from the spec: `deploy.image` and `deploy.env`
   as given, `deploy.resources` as a size hint clamped to the lab's device limits (the run is refused when it cannot fit at half of
   the hint or more), and `deploy.pull` as a pull secret of that lab only (never stored or logged). The tools device sits at the
   foothold switch (attacker) or beside the devices it operates (defender), with the control gateway. `StartRun` carries the
   `spec_id` and the fixed inputs: `access.vpn{entry, addr}` and `foothold` (an attacker without a foothold is refused before deploy).
   `testdata/brain/wire/start_run_opponent.json` is the exact message. A brain that answers `GetOpponent` with `Unimplemented`
   gets the deprecated fallback: `BRAIN_TOOLS_IMAGE`, no `spec_id`.
   Several opponents of the variant can play together in ONE lab
   (`POST /api/exercises/{id}/versions/{versionID}/variants/{variantID}/opponents/try` with `{"indexes":[1,2]}`, at most 8): every
   opponent gets a run of its own (its own `run_id`, `GetOpponent` call, spec, VPN client and `StartRun` with its own
   `access.vpn.addr`), the lab gets one tools device per opponent (`tools`, `tools-2`, ...; the device of the opponent at
   position i of the list has index i) and one control gateway; `tasks/opponent-lab/deployed-topology-both.json` is that lab. Each
   run is read and stopped under its own `.../opponents/{index}/try/{runID}`. A run that ends early revokes only its own client;
   the lab is destroyed when the last of its runs ends.
4. A run the brain does not reach, or that outlives `BRAIN_RUN_MAX_DURATION`, ends as failed; the daemon always revokes the run's
   client and destroys the lab (a shared lab once its last run has ended), and never waits on the brain to do it.

## 8. Without the daemon: replaying the samples

`testdata/brain/wire/*.json` are the exact messages as proto-JSON (protojson with the proto field names). A gRPC receiver can be
checked with a client certificate from your own enrollment and `grpcurl` (not pinned in this repository), for example:

```sh
grpcurl -cacert /tmp/brain-ca.crt -cert client.crt -key client.key -import-path pkg/brainapi/v1 -proto brain.proto \
  -d @ localhost:9099 cybericebox.brain.v1.Brain/PushData < testdata/brain/wire/push_data_web_dynamic.json
```

`PushData` items carry the aggregate as bytes, so in proto-JSON `aggregates[].json` is base64 of the backend's `Aggregate` JSON
(`jq -r '.aggregates[0].json | @base64d'` shows it).
