# The create archive (a brand-new task)

`sample.cybericebox.zip` is the archive of a NEW task in the platform's import format. `exercise.json` and `entries.txt` next to it are
the unpacked document and the list of entries. All three are made by `TestBrainCreateSample` from the daemon's own code (the edit sample
of the same task with every platform id taken out and the secret variable `DB_PASSWORD` declared with an empty value); regenerate with
`go test ./internal/useCase/exercise -run TestBrainCreateSample -update-brain-fixtures`. For a small hand-written, publishable create see
`../tasks/web-dynamic/exercise.yaml` (a dynamic task with one web device). The edit side is `../edit/README.md`.

## The container

* A **ZIP** (deflate), never a tar/gzip: `archive.go:871` (`ReadArchiveV1`) opens it with `archive/zip`; a `.tar.gz` is refused ("not a valid zip file") and has no converter.
* Exactly **one task folder**: the zip root, or one directory, that holds `exercise.json` **or** `exercise.yaml` (both is an error;
  several folders = "import it as a bundle"; a folder inside another is refused) — `archive_layout.go:90` (`splitTaskFolders`), `archive_jsonc.go:59`
  (`pickExerciseDocument`).
* `manifest.json` at the root is **optional** (`{"version":1,"checksums":{"<entry>":"<sha256 hex>"}}`); when present every listed entry is
  checked (`archive.go:1021`). The daemon adds it itself when it builds an archive.
* Entry names: relative, no `..`, no symlinks, no duplicates (NFC-normalized; a backslash is a slash); `__MACOSX/`, `.DS_Store`,
  `Thumbs.db`, `desktop.ini` are dropped (`archive_layout.go:45-86`). Limits: 512 MiB archive, 1000 entries by default (the platform
  settings may change them); the document itself is capped (`maxExerciseDocBytes`).
* Device **image files** inside the archive (`image: {"file": "..."}`) make the import a background job; a plain reference
  (`"image": "nginx:1.27"`) does not.

## The document `exercise.json` (or `exercise.yaml`, same keys)

The importer decodes it **strictly**: the exact, case-sensitive keys below only, no trailing data; an unknown key is an error
(`decodeArchiveV2` in `archive_v2.go`, with `checkArchiveKeys` over the Go structs). JSON comments are accepted in `exercise.json` (JSONC).
`schema` must be exactly `"cib-exercise/1.0"`. **The Go structs are the schema:** `internal/useCase/exercise/archive_v2.go`
(`archiveDocV2` and the `archive*V2` document structs); the domain types they reuse (`Placeholder`, `Interface`, `Access`, `Opponent`, observations) are
in `internal/model/exercise/`. A **JSON Schema (draft 2020-12)** is generated from
those structs: `testdata/brain/schema/cib-exercise-1.0.schema.json` (`go generate ./internal/useCase/exercise`; a test fails when it drifts, and
the samples and `tasks/web-dynamic` are validated against it). It covers keys, types, enums and the keys that cannot be omitted, with
`additionalProperties: false` everywhere. Strip JSONC comments before validating. Publish-time rules (a check on every task, a resolving device graph,
limits, linked files) are not in the schema; the prose below lists them.

```
document   {schema, exercise{id?,name,description?,tags?}, versions[]}
version    {id? (ignored), status? (draft|published|checkpoint), exercise? , admin_note?, label?, variants[]}
variant    {id?, index?, note?, linked_to?, weight?, device_images?, tasks[], topology, opponents?}
task       {id?, key?, name, description (rich text JSON or text), difficulty, requires[]?, check, attachments[]?, placeholders[]?, hints[]?}
check      exactly one of  answer {type: flag|text, values[], input_hint?, ignore_case?, trim?}
                          state  {mode: once|continuous, interval?, manual?, conditions[]}
hint       {id?, text, level: nudge|direction|steps|near_solution}
topology   {variables[]?, devices[], connections[], visual_render?}
device     {id?, name, type: node|unmanaged-switch|hub|vpn_gateway|internet_gateway (container = old spelling of node),
            profile: container|microvm|vm, image (ref string | {file}), security_preset: standard|extended, resource_preset,
            interfaces[], command[], args[], system, hardware, bootstrap?, access[], persistence?, wait_ready_file,
            dhcp, dhcp_ranges[], dns, reverse_connections}
bootstrap  {timeout?, env[{name, value, secret?}], files[{path, content|source, owner?, group?, mode?}], script?, interpreter?}
connection {endpoints[{kind: device, device (name), interface}]}
attachment {file (path in the task folder), name?}
```

* **What the importer requires** (parse and `validateImportContent`, `archive.go:222`): `schema`, `exercise.name` (the platform's name
  and description limits apply), at least one version (`versions: at least one version is required`), enum values spelled as above
  (`difficulty`: `elementary|trivial|easy|medium|hard|insane`), a `name` on every task and a `name` and `type` on every device,
  device names unique inside a variant, every linked file present in the archive, every device and task a connection,
  placeholder or condition names resolving to something in the document. Nothing else is needed: an incomplete **draft** imports.
* **What the author is held to at publish, not at import** (`ValidateForPublish`, `internal/model/exercise/version.go`): exactly one of
  `answer`/`state` in every task's `check`, a non-empty description, a device graph that holds (connections resolve), placeholders that
  resolve, a single fixed flag value unless the flag is delivered to a device, task name 3-50 characters, **and no secret variable
  without a value**. A version imported from an archive always lands as a draft, whatever `status` it names.
* **Ids are optional everywhere** (`exercise.id`, `variants[].id`, `tasks[].id`, `hints[].id`, `devices[].id`): the platform gives its own.
  Devices are addressed by **name** (connections, placeholders, state conditions); `device_id` in an endpoint is only for a re-import.
  A task `key` is a slug `^[a-z][a-z0-9_]{0,31}$` (`bootstrap.go:249`), made by the platform when absent; `requires` and the
  `{{task.<key>}}` substitution use it.
* **Fields that are NOT in the schema** (for example `story`, `kind`, `category`): refused as unknown keys. Use
  `exercise.name`, `exercise.description` and `exercise.tags` for what a card shows; there is no category or kind field.
* **Secret variables:** a `bootstrap.env[]` entry with `"secret": true`, declared with an **empty** `value`; a non-empty secret value in an archive that comes
  back from an edit is refused (the save fails). The platform lists the secrets
  without a value as the version's *missing secret values*; the author enters them in the editor and the version cannot be published
  before. Ordinary variables carry their values.

## Where files go (`files/`)

The importer accepts any relative path for a file the document links (`attachments[].file`, `bootstrap.files[].source`, `image.file`),
relative to the task folder. The export writes this convention (`archive_export_paths.go`, `assignExportPaths`), and an archive may use
it or any other: `files/v<V>/variant-<N>/<T>/<name>` for a file of one subtask, `files/v<V>/shared/<name>` for one used in several
variants of a version, `files/shared/<name>` for one used in several versions. `<V>` is the 1-based position of the version in
`versions[]`, `<N>` the 1-based position of the variant in the version, `<T>` the 1-based position of the task in the variant; the
same bytes are stored once. A new task has one version, so `files/v1/...`.
