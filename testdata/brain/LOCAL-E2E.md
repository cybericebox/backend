# Running the daemon against cyber-brain locally

A recipe for an end-to-end run on one machine: the daemon (host process), PostgreSQL and a laboratory from the local stand, and a
brain process. The wire it speaks is pinned in `cyber-brain/docs/daemon-interface.md`; the samples are in
[`testdata/brain`](README.md). What is **not** verified here is marked.

## Who dials whom

```
 browser --cookie--> daemon --mTLS HTTP/SSE--> brain      (the daemon is ALWAYS the client of the brain)
                        |                        |
                        | gRPC mTLS              | WireGuard (the run's own client, per run)
                        v                        v
                    laboratory agent -----> lab network <-- the brain reaches the tools device runner at ONE address (a /32 on `ctl`)
```

- **The daemon dials the brain**, never the reverse. Enrollment, `POST /v1/runs` (StartRun), materials, verdicts, stop, the run
  and chat event streams, `GET /v1/requests/watch` (the brain's outbox: `provide_lab`, `provide_device`, `provide_data`),
  `POST /v1/requests/{id}/answer`, `POST /v1/data` (PushData) and `POST /v1/runs/{id}/lab_facts` are all daemon-to-brain HTTP.
  The brain must be reachable from the daemon; it never calls the daemon.
- **The brain dials the lab**: `access.vpn.entry` of StartRun is the full WireGuard config of a client the daemon made for that run
  (its own key, revoked at teardown). The brain brings the tunnel up and speaks to the lab as a participant. For an opponent,
  `access.addr` is `ip:port` of the runner agent on the tools device, on the hidden control gateway `ctl`; the run's tunnel is
  restricted to exactly that one address. The runner authenticates the brain's per-run credential (`EDA_RUN_ID` and `EDA_TRUST_CA`
  are set on the tools device by the daemon). So the machine that runs the brain must reach the **laboratory's VPN endpoint** (the
  UDP address the agent publishes), and WireGuard must be usable there (root or CAP_NET_ADMIN; a rootless run is not verified).
- The daemon talks to the laboratory agent over gRPC mTLS as for any lab; the brain never touches infrastructure.

## 1. Prerequisites

1. The local stand of the infrastructure repository (`make host-up` in `infrastructure/local`): PostgreSQL, the object store, a
   cluster with the laboratory agent. The daemon README ("Running locally") lists what the environment file needs; a working lab
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
| `BRAIN_ENDPOINT` | `localhost:8099` (host:port of the brain, HTTPS; empty switches the assistant off) |
| `BRAIN_ENROLLMENT_TOKEN` | the token above (read once; ignored after the first enrollment) |
| `BRAIN_CA_FILE` | `/tmp/brain-ca.crt` (bootstrap trust for the first enrollment; empty: the system roots) |
| `BRAIN_TOOLS_IMAGE` | the platform tools image (no default; without it `provide_device` for tools and opponent runs answer `not_supported_yet`) |
| `BRAIN_RUN_GRACE` | `60s` (how long an unreachable brain is waited for before a run fails) |
| `BRAIN_RUN_MAX_DURATION` | `30m` (the longest a run may last, its lab start included) |
| `BRAIN_OPPONENT_ACTIONS`, `BRAIN_OPPONENT_TOKENS`, `BRAIN_OPPONENT_SECONDS` | `100`, `500000`, `1200` (the budget every opponent run gets; the author never sets one) |
| `BRAIN_CONSOLE_MAX_LIFETIME` | `8h` (caps a console session that never reported its end, in the statistics) |

Then, from the daemon directory:

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
- In the brain's log: one `POST /v1/enroll`, then mTLS requests; `GET /v1/requests/watch` stays open (the daemon's outbox stream).

## 6. Statistics (PushData)

Collection is **off** until a platform administrator turns it on, per source and per event:

```
PUT /api/admin/assistant/statistics/defaults/events        {"Mode": "continuous"}    # off | final | continuous
PUT /api/admin/assistant/statistics/events/{eventID}       {"Mode": "default"}       # default follows the platform
```

`final` reports an event once it has ended, `continuous` while it runs. The leader pushes every hour; the brain can also ask for
fresh figures with a `provide_data` request in its outbox (answered at once). The effort figures need the laboratory's activity
reports (`activityReports.enabled` in the laboratory chart); without them they are `null`. Before enabling this on a real
installation the Privacy Policy wording gets its legal review.

## 7. Grader and opponent runs

1. Import `testdata/brain/tasks/web-dynamic` (zip the folder with `exercise.yaml` at its root; `POST /api/exercises/import` or the
   exercises site) and publish it. The grader needs a working lab (an image the cluster can pull, here `nginx:1.27`).
2. Start a grader run on its variant (`POST /api/exercises/{id}/versions/{versionID}/variants/{variantID}/measure`). The daemon
   deploys a test lab, makes the run's own VPN client, uploads the materials (`POST /v1/runs/{id}/materials`), then `POST /v1/runs`.
   It follows the run's events, judges each `answer` event with the platform checker and replies with `POST /v1/runs/{id}/verdict`
   (only `correct`, never the expected value); at the end it sends `lab_facts` and destroys the lab.
3. For an opponent import and publish `testdata/brain/tasks/opponent-lab`, set `BRAIN_TOOLS_IMAGE`, and try an opponent
   (`POST /api/exercises/{id}/versions/{versionID}/variants/{variantID}/opponents/{index}/try`). The lab is deployed with the tools
   device (the attacker at the foothold switch, the defender beside the devices it operates) and the control gateway; `StartRun`
   carries `access: {vpn, addr}`. `testdata/brain/wire/start_run_opponent.json` is the exact body.
4. A run the brain does not reach, or that outlives `BRAIN_RUN_MAX_DURATION`, ends as failed; the daemon always revokes the run's
   client and destroys the lab, and never waits on the brain to do it.

## 8. Without the daemon: replaying the samples

`testdata/brain/wire/*.json` are the exact bodies. A PushData receiver can be checked with a client certificate from your own
enrollment, for example:

```sh
curl --cacert /tmp/brain-ca.crt --cert client.crt --key client.key -H 'Content-Type: application/json' \
  --data @testdata/brain/wire/push_data_web_dynamic.json https://localhost:8099/v1/data
```

Run events and the outbox are Server-Sent Events: `id: <seq>`, `event: <type>`, `data: <json>`, resumed with `?after=<seq>`.
