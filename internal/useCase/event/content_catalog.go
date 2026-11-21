package event

import (
	"sort"

	eventConfigModel "github.com/cybericebox/daemon/internal/model/eventConfig"
	eventContentModel "github.com/cybericebox/daemon/internal/model/eventContent"
	"github.com/cybericebox/daemon/internal/model/usertext"
)

// ContentVariableDefinition is the server-owned editor contract for a value
// that the current event-content renderer can resolve.
type ContentVariableDefinition struct {
	Name     string                           `json:"name"`
	Label    string                           `json:"label"`
	Format   eventContentModel.VariableFormat `json:"format"`
	Audience eventContentModel.PageVisibility `json:"audience"`
}

var contentVariableLabels = usertext.ContentVariableLabels

func ContentVariableCatalog(scoreboardVisibility eventConfigModel.Visibility) []ContentVariableDefinition {
	items := make([]ContentVariableDefinition, 0, len(eventContentModel.EventContentVariables))
	for name, format := range eventContentModel.EventContentVariables {
		audience, known := contentVariableAudiences[name]
		if !known {
			audience = eventContentModel.PageVisibilityManager
		}
		if name == "event.solveCount" || name == "event.solvedChallengeCount" {
			switch scoreboardVisibility {
			case eventConfigModel.VisibilityPublic:
				audience = eventContentModel.PageVisibilityPublic
			case eventConfigModel.VisibilityPrivate:
				audience = eventContentModel.PageVisibilityParticipant
			default:
				audience = eventContentModel.PageVisibilityManager
			}
		}
		label := contentVariableLabels[name]
		if label == "" {
			label = name
		}
		items = append(items, ContentVariableDefinition{Name: name, Label: label, Format: format, Audience: audience})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	return items
}
