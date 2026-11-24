# The edit archive (an existing task exported for editing)

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

## What the import does with an archive that comes back

* **Same exercise id, and the author may edit that task:** the archive is previewed and committed as an update of that task (the
  the edit replaces the task's working copy, which it started from). Variants are paired by id, then by order; tasks by id, then key; devices by
  name. An empty secret on a variable the stored version has (same variant, device name, variable name) keeps its stored value.
  A secret variable that is new (a new device or name) stays empty and is a **missing secret value** of the version: the catalog
  shows it, and the version cannot be published until the author enters it.
* **The working copy moved on since the export** (someone saved it, or made one when there was none, or discarded it): the save
  is refused with the concurrent-edit conflict (409, code 70922) and the current revision, who saved it and when in its context;
  nothing is written. A task that has a working copy is always exported as that working copy (whichever version was asked for), so
  an edit never replaces work it does not contain; a task without one is exported as the version asked for, and the save then
  requires that there is still none.
* **No `exercise.id`, an unknown one, one the author may not edit, or an archived task:** the archive is imported as a NEW task
  (and then every secret variable is missing until entered). Never an error, never an update of a task the author cannot edit.
* **A changed `variants[].id`** is not an error either: a variant whose id is not found is paired by order with the next unmatched
  stored variant; if there is none it is a new variant. Its stored secrets are found by that pairing, not by the id the document
  carries.
* **A changed device or variable name** loses its stored secret (it is a new key): the value is missing and has to be entered.
