package exerciseModel

// WarnAttachmentsInSomeVariants: a subtask has files in some independent variants and none in others. Teams that get
// a variant without files may miss material the others have; it never stops a publish or an import (the same gap in
// a linked variant is the error ErrLinkedAttachmentMissing).
const WarnAttachmentsInSomeVariants = "attachments_in_some_variants"

// ContentWarning is something about the content of a version an author should know; it never forbids. Task is the
// key of the subtask (its name when it has no key); WithFiles and WithoutFiles are the 1-based positions of the
// variants in the version (as the "variant" context of the errors counts them). The slices are never null.
type ContentWarning struct {
	Code         string `json:"Code"`
	Task         string `json:"Task"`
	WithFiles    []int  `json:"WithFiles"`
	WithoutFiles []int  `json:"WithoutFiles"`
}

// ContentWarnings lists the content warnings of the version, in the order of its subtasks; never nil.
func (v *ExerciseVersion) ContentWarnings() []ContentWarning { return ContentWarnings(v.Variants) }

// ContentWarnings compares the independent variants (not linked to a base) subtask by subtask, by position (the
// variants of a version have the same number of subtasks): a subtask with files in some of them and none in the
// others is one warning. Linked variants are left out: their files are checked against their base on publish.
func ContentWarnings(variants []Variant) []ContentWarning {
	out := []ContentWarning{}
	tasks := 0
	for _, variant := range variants {
		if !variant.IsLinked() {
			tasks = max(tasks, len(variant.Tasks))
		}
	}
	for position := range tasks {
		warning := ContentWarning{Code: WarnAttachmentsInSomeVariants, WithFiles: []int{}, WithoutFiles: []int{}}
		for i, variant := range variants {
			if variant.IsLinked() || position >= len(variant.Tasks) {
				continue
			}
			task := variant.Tasks[position]
			if warning.Task == "" {
				warning.Task = task.Key
				if warning.Task == "" {
					warning.Task = task.Name
				}
			}
			if len(task.Attachments) > 0 {
				warning.WithFiles = append(warning.WithFiles, i+1)
			} else {
				warning.WithoutFiles = append(warning.WithoutFiles, i+1)
			}
		}
		if len(warning.WithFiles) > 0 && len(warning.WithoutFiles) > 0 {
			out = append(out, warning)
		}
	}
	return out
}
