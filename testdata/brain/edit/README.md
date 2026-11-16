# The edit archive (what the brain gets to edit an existing task)

`sample.cybericebox.zip` is what the platform answers a `provide_task` / `SendTask` request with (`ExportTaskVersionForEdit`):
the import archive of ONE version of the task, in the format of the admin export, without secret values. `exercise.json` and
`entries.txt` next to it are the unpacked document and the list of the archive's entries. All three are written by
`TestBrainEditSample` from the daemon's own code, so they cannot drift (regenerate with
`go test ./internal/useCase/exercise -run TestBrainEditSample -update-brain-fixtures`).

The sample task: one draft version, two variants, a device `web` with an ordinary variable `MODE` and a secret variable
`DB_PASSWORD` (exported with an empty value), attachments in `files/`.

## What identifies what

| Thing | Where | Identity |
|---|---|---|
| The task | `exercise.json` -> `exercise.id` | the exercise id (a UUID): the task this is an edit of |
| A variant | `versions[0].variants[i].id` | the variant id (stable). `index` is only the display number (1, 2, ...) and `note` free text; neither identifies anything |
| A task of a variant | `variants[i].tasks[j].id`, then `key` | id, then key. Hints: `hints[k].id`, then position |
| A device | `variants[i].topology.devices[d].name` | the NAME only (the `id` is informational). A renamed device is a removed one and a new one |
| A variable | `devices[d].bootstrap.env[e].name` | the name, inside its device |
| A secret variable | `devices[d].bootstrap.env[e].secret == true` | same key (variant, device name, variable name); the value is empty |

## What the brain must keep unchanged

* `exercise.id` (kept verbatim: it is what links the edit to the task) and every `variants[].id`, `tasks[].id`, `hints[].id` of what
  stays. Do not renumber, regenerate or "normalize" them. New variants, tasks, devices and hints may be added without an id.
* The `edit` object (`edit.version` and `edit.draft_revision`): what the edit started from, the version it was given and the
  revision of the task's working copy then (empty: it had none). Both are opaque; keep them as they are. The save of the edit is
  applied only on top of that working copy (see below).
* The names of devices and variables that stay, and `secret: true` on a secret variable.
* The value of a secret variable: **empty**. The brain never sets a secret value and never gets one. A secret variable with a
  non-empty value in the archive the brain gives back is refused (the save fails with the code "the finished task carries a secret
  value"), for an edited task and for a new one alike. A secret the brain adds is declared the same way: `secret: true`, empty value.

## What the daemon does with the archive that comes back

* **Same exercise id, and the author may edit that task:** the archive is previewed and committed as an update of that task (the
  author confirms it when it replaces a working draft that the edit did not start from). Variants are paired by id, then by order; tasks by id, then key; devices by
  name. An empty secret on a variable the stored version has (same variant, device name, variable name) keeps its stored value.
  A secret variable that is new (a new device or name) stays empty and is a **missing secret value** of the version: the catalog
  shows it, and the version cannot be published until the author enters it.
* **The working copy moved on since the export** (someone saved it, or made one when there was none, or discarded it): the save
  is refused with the concurrent-edit conflict (409, code 70922) and the current revision, who saved it and when in its context;
  nothing is written. An edit of the working copy itself, still at the revision the export saw, continues it. An edit that started
  from another version (the published one) and would replace a working copy the author has is confirmed by the author first.
* **No `exercise.id`, an unknown one, one the author may not edit, or an archived task:** the archive is imported as a NEW task
  (and then every secret variable is missing until entered). Never an error, never an update of a task the author cannot edit.
* **A changed `variants[].id`** is not an error either: a variant whose id is not found is paired by order with the next unmatched
  stored variant; if there is none it is a new variant. Its stored secrets are found by that pairing, not by the id the document
  carries.
* **A changed device or variable name** loses its stored secret (it is a new key): the value is missing and has to be entered.

The platform serves only the tenant of the brain's certificate (the CN): requests and the archive upload of one tenant never cross
into another's.
