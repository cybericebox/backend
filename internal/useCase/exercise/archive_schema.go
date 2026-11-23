package exercise

//go:generate go run ../../../tools/archiveschema -schema cib-exercise/1.0 -o ../../../testdata/brain/schema/cib-exercise-1.0.schema.json
//go:generate go run ../../../tools/archiveschema -schema cib-exercise/1.1 -o ../../../testdata/brain/schema/cib-exercise-1.1.schema.json

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"sort"
	"strings"

	exerciseModel "github.com/cybericebox/backend/internal/model/exercise"
)

// ArchiveDocumentSchema is the JSON Schema (draft 2020-12) of the exercise document an import reads (`exercise.json`, or the same
// keys in `exercise.yaml`), generated from the Go structs of archive_v2.go and the domain types they reuse. It describes the SHAPE
// the strict decoder accepts: the exact, case-sensitive keys, their types and the enum spellings, and the keys a document cannot do
// without. The rules that need the whole task (a check on every task, a resolving device graph, a single flag, names and limits, no
// secret without a value at publish) are checked by the platform on import and publish, not here; testdata/brain/create/README.md
// lists them. The output is deterministic (generate it with `go generate ./internal/useCase/exercise`; a test fails on drift).
// schema is a version the import reads (ArchiveSchemas); the keys of a later version are left out of an earlier one.
func ArchiveDocumentSchema(schema string) ([]byte, error) {
	if !archiveSchemaSupported(schema) {
		return nil, fmt.Errorf("archive schema: unknown version %q", schema)
	}
	g := &schemaGenerator{defs: map[string]any{}, names: map[reflect.Type]string{}, schema: schema}
	root := g.ref(reflect.TypeFor[archiveDocV2]())
	doc := map[string]any{
		"$schema":     "https://json-schema.org/draft/2020-12/schema",
		"title":       schema,
		"description": "The exercise document of a task archive (exercise.json). Generated from internal/useCase/exercise/archive_v2.go; the platform checks the rest (publish rules, limits, linked files) on import and publish.",
		"$ref":        root["$ref"],
		"$defs":       g.defs,
	}
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(doc); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// schemaRequired lists, by type, the keys a document cannot do without (what the importer refuses to go on without); every other
// key is optional.
var schemaRequired = map[string][]string{
	"archiveDocV2":           {"schema", "exercise", "versions"},
	"archiveExerciseV2":      {"name"},
	"archiveVersionV2":       {"variants"},
	"archiveTaskV2":          {"name"},
	"archiveHintV2":          {"text"},
	"archiveAttachmentV2":    {"file"},
	"archiveDeviceV2":        {"name", "type"},
	"archiveEnvVarV2":        {"name"},
	"archiveBootstrapFileV2": {"path"},
	"archiveVariableV2":      {"name"},
	"archiveConnectionV2":    {"endpoints"},
	"archiveEndpointV2":      {"kind"},
	"archiveConditionV2":     {"device"},
	"archiveFileCheckV2":     {"path"},
	"archiveCustomCheckV2":   {"run"},
	"Placeholder":            {"kind"},
	"ConsolePort":            {"name"},
	"NativeSource":           {"kind"},
	"SerialPort":             {"name"},
	"DefenderCredential":     {"device"},
}

// schemaEnums are the spellings of the named string types of the document, from the domain's own constants.
var schemaEnums = map[reflect.Type][]string{
	reflect.TypeFor[exerciseModel.Difficulty](): {
		string(exerciseModel.DifficultyElementary), string(exerciseModel.DifficultyTrivial), string(exerciseModel.DifficultyEasy),
		string(exerciseModel.DifficultyMedium), string(exerciseModel.DifficultyHard), string(exerciseModel.DifficultyInsane),
	},
	reflect.TypeFor[exerciseModel.HintLevel](): {
		string(exerciseModel.HintLevelNudge), string(exerciseModel.HintLevelDirection), string(exerciseModel.HintLevelSteps), string(exerciseModel.HintLevelNearSolution),
	},
	reflect.TypeFor[exerciseModel.AnswerType](): {string(exerciseModel.AnswerTypeFlag), string(exerciseModel.AnswerTypeText)},
	reflect.TypeFor[exerciseModel.StateMode]():  {string(exerciseModel.StateModeOnce), string(exerciseModel.StateModeContinuous)},
	reflect.TypeFor[exerciseModel.DeviceType](): {
		string(exerciseModel.DeviceTypeNode), string(exerciseModel.DeviceTypeContainer), string(exerciseModel.DeviceTypeUnmanagedSwitch),
		string(exerciseModel.DeviceTypeHub), string(exerciseModel.DeviceTypeVPNGateway), string(exerciseModel.DeviceTypeInternetGateway),
	},
	reflect.TypeFor[exerciseModel.NodeProfile](): {
		string(exerciseModel.NodeProfileContainer), string(exerciseModel.NodeProfileMicroVM), string(exerciseModel.NodeProfileVM),
	},
	reflect.TypeFor[exerciseModel.SecurityPreset](): {string(exerciseModel.SecurityPresetStandard), string(exerciseModel.SecurityPresetExtended)},
	reflect.TypeFor[exerciseModel.EndpointKind]():   {string(exerciseModel.EndpointDevice)},
	reflect.TypeFor[exerciseModel.ConnectionKind](): {string(exerciseModel.ConnectionConsole)},
	// vm-serial is the platform's (the vm profile): an author never writes it.
	reflect.TypeFor[exerciseModel.NativeKind](): {string(exerciseModel.NativeStdio), string(exerciseModel.NativeTCP)},
	// The topology the defender's dossier shows: a closed enum (confirmed with the brain session).
	reflect.TypeFor[exerciseModel.DefenderTopology](): {
		string(exerciseModel.DefenderTopologyFull), string(exerciseModel.DefenderTopologyOperates), string(exerciseModel.DefenderTopologyNone),
	},
	reflect.TypeFor[exerciseModel.VersionStatus](): {
		string(exerciseModel.VersionStatusDraft), string(exerciseModel.VersionStatusPublished),
		string(exerciseModel.VersionStatusUnpublished), string(exerciseModel.VersionStatusCheckpoint),
	},
	reflect.TypeFor[exerciseModel.AccessKind](): {
		string(exerciseModel.AccessHTTP), string(exerciseModel.AccessTerminal), string(exerciseModel.AccessVNC), string(exerciseModel.AccessRDP),
	},
	reflect.TypeFor[exerciseModel.SystemKind]():   {string(exerciseModel.SystemLinux), string(exerciseModel.SystemWindows)},
	reflect.TypeFor[exerciseModel.HardwareKind](): {string(exerciseModel.HardwareFast), string(exerciseModel.HardwareCompatible)},
	reflect.TypeFor[exerciseModel.PlaceholderKind](): {
		string(exerciseModel.PlaceholderGatewaySubnet), string(exerciseModel.PlaceholderIP), string(exerciseModel.PlaceholderExternalLink),
	},
}

type schemaGenerator struct {
	defs   map[string]any
	names  map[reflect.Type]string
	schema string
}

// ArchiveSchemas lists the document versions the import reads, oldest first.
func ArchiveSchemas() []string { return slices.Clone(archiveSchemas) }

// ref registers a named struct once under $defs and returns its reference.
func (g *schemaGenerator) ref(t reflect.Type) map[string]any {
	name, known := g.names[t]
	if !known {
		name = t.Name()
		for other, used := range g.names {
			if used == name && other != t {
				name = strings.ReplaceAll(t.PkgPath(), "/", ".") + "." + t.Name()
			}
		}
		g.names[t] = name
		g.defs[name] = nil // reserved: a type that refers to itself finds its name
		g.defs[name] = g.object(t, name)
	}
	return map[string]any{"$ref": "#/$defs/" + name}
}

func (g *schemaGenerator) object(t reflect.Type, name string) map[string]any {
	properties := map[string]any{}
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		key, skip := jsonKey(field)
		if skip || !keyInSchema(field, g.schema) {
			continue
		}
		property := g.of(field.Type)
		if name == "archiveDocV2" && key == "schema" {
			property = map[string]any{"const": g.schema}
		}
		properties[key] = property
	}
	object := map[string]any{"type": "object", "additionalProperties": false, "properties": properties}
	if required := schemaRequired[name]; len(required) > 0 {
		sorted := append([]string(nil), required...)
		sort.Strings(sorted)
		for _, key := range sorted {
			if _, ok := properties[key]; !ok {
				panic(fmt.Sprintf("archive schema: %s has no key %q", name, key))
			}
		}
		object["required"] = sorted
	}
	return object
}

// of is the schema of one Go type.
func (g *schemaGenerator) of(t reflect.Type) any {
	switch t {
	case reflect.TypeFor[archiveInterval]():
		return map[string]any{"oneOf": []any{map[string]any{"type": "string"}, map[string]any{"const": 0}}, "description": "a duration such as 30s, or 0 for no schedule"}
	case reflect.TypeFor[archiveImageV2]():
		return map[string]any{"oneOf": []any{
			map[string]any{"type": "string", "description": "an image reference such as nginx:1.27"},
			map[string]any{"type": "object", "additionalProperties": false, "required": []string{"file"}, "properties": map[string]any{"file": map[string]any{"type": "string"}}, "description": "an image file shipped in the archive"},
		}}
	case reflect.TypeFor[json.RawMessage]():
		return map[string]any{}
	}
	if values, ok := schemaEnums[t]; ok {
		return map[string]any{"type": "string", "enum": values}
	}
	switch t.Kind() {
	case reflect.Pointer:
		return g.of(t.Elem())
	case reflect.Struct:
		return g.ref(t)
	case reflect.Slice:
		return map[string]any{"type": "array", "items": g.of(t.Elem())}
	case reflect.Map:
		return map[string]any{"type": "object", "additionalProperties": g.of(t.Elem())}
	case reflect.String:
		return map[string]any{"type": "string"}
	case reflect.Bool:
		return map[string]any{"type": "boolean"}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64, reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return map[string]any{"type": "integer"}
	case reflect.Float32, reflect.Float64:
		return map[string]any{"type": "number"}
	case reflect.Interface:
		return map[string]any{}
	}
	panic(fmt.Sprintf("archive schema: no schema for %s", t))
}

// jsonKey is the key of a field in the document (its json tag), or skip for a field the document does not carry.
func jsonKey(field reflect.StructField) (key string, skip bool) {
	if !field.IsExported() {
		return "", true
	}
	tag := field.Tag.Get("json")
	if tag == "-" {
		return "", true
	}
	name, _, _ := strings.Cut(tag, ",")
	if name == "" {
		return field.Name, false
	}
	return name, false
}
