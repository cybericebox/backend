package exercise

import (
	"context"
	"encoding/json"
	"io"
	"strconv"
	"strings"

	"github.com/gofrs/uuid"

	"github.com/cybericebox/daemon/internal/model"
	exerciseModel "github.com/cybericebox/daemon/internal/model/exercise"
)

// The participant's view of a task for a run: what a person who joins the test lab is shown, as text and files. It is
// built from an allowlist (the title, the description, and per subtask the key, the name, the rendered text and the kind
// of its answer, plus the attachments a participant downloads), never by removing things from the author's document, so a
// hint, an author note, an expected answer, an opponent or a check can not get in whatever the document holds.

// Materials is what the brain is given before it starts a run: Task is task.json, Files the attachments.
type Materials struct {
	Task  []byte
	Files []MaterialFile
}

// MaterialFile is one attachment as the participant sees it.
type MaterialFile struct {
	Name        string
	ContentType string
	Data        []byte
}

type (
	materialsTask struct {
		Title       string             `json:"title"`
		Description string             `json:"description"`
		Subtasks    []materialsSubtask `json:"subtasks"`
	}
	materialsSubtask struct {
		Key    string `json:"key"`
		Title  string `json:"title"`
		Prompt string `json:"prompt"`
		// AnswerKind is how the subtask is judged: flag (an answer ICE{...}), text (a free answer, compared as the
		// participant's checker does) or state (nothing is submitted: the platform checks the lab).
		AnswerKind string `json:"answer_kind"`
	}
)

// RunMaterials builds the participant's view of the task of the owner's test lab: the texts with the placeholders resolved
// for that lab, and the attachments read from the file store under the archive size limits (one file, and all together).
func (u *ExerciseUseCase) RunMaterials(ctx context.Context, owner, deployID uuid.UUID) (Materials, error) {
	status, deploy, err := u.deployTestStatus(ctx, owner, deployID)
	if err != nil {
		return Materials{}, err
	}
	_, variant, _, err := u.loadVersionVariant(ctx, deploy.VersionID, deploy.VariantID)
	if err != nil {
		return Materials{}, err
	}
	// A participant sees the name and the text of each subtask only (the event's task snapshot carries nothing of the exercise
	// itself), so the title is the name of the first subtask and there is no text above the subtasks.
	doc := materialsTask{Subtasks: make([]materialsSubtask, 0, len(variant.Tasks))}
	if len(variant.Tasks) > 0 {
		doc.Title = strings.TrimSpace(variant.Tasks[0].Name)
	}
	var refs []exerciseModel.AttachmentRef
	for _, task := range variant.Tasks {
		sub := materialsSubtask{Key: task.Key, Title: task.Name, Prompt: renderDescription(task.Description, placeholderResolver(task.Placeholders, status))}
		sub.AnswerKind = answerKind(task)
		doc.Subtasks = append(doc.Subtasks, sub)
		refs = append(refs, task.Attachments...)
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		return Materials{}, model.ErrPlatform.WithError(err).WithMessage("Failed to build the task materials").Err()
	}
	files, err := u.readAttachments(ctx, refs)
	if err != nil {
		return Materials{}, err
	}
	return Materials{Task: raw, Files: files}, nil
}

// readAttachments reads the files a participant downloads. Each file is checked against its stored size before it is
// read, one by the entry limit of an archive and all together by the unpacked limit.
func (u *ExerciseUseCase) readAttachments(ctx context.Context, refs []exerciseModel.AttachmentRef) ([]MaterialFile, error) {
	if len(refs) == 0 {
		return nil, nil
	}
	if u.files == nil {
		return nil, model.ErrPlatform.WithMessage("File storage is not configured").Err()
	}
	limits := u.ArchiveLimits(ctx)
	var total int64
	out := make([]MaterialFile, 0, len(refs))
	for _, ref := range refs {
		meta, err := u.files.GetFile(ctx, ref.FileID)
		if err != nil {
			return nil, model.ErrPlatform.WithError(err).WithMessage("Failed to read an attachment").Err()
		}
		total += meta.SizeBytes
		if meta.SizeBytes > limits.EntryBytes || total > limits.UnpackedBytes {
			return nil, exerciseModel.ErrArchiveTooLarge.Err()
		}
		rc, _, err := u.files.StreamFile(ctx, ref.FileID)
		if err != nil {
			return nil, model.ErrPlatform.WithError(err).WithMessage("Failed to read an attachment").Err()
		}
		data, err := io.ReadAll(io.LimitReader(rc, meta.SizeBytes+1))
		_ = rc.Close()
		if err != nil || int64(len(data)) != meta.SizeBytes {
			return nil, model.ErrPlatform.WithError(err).WithMessage("Failed to read an attachment").Err()
		}
		out = append(out, MaterialFile{Name: ref.Name, ContentType: meta.ContentType, Data: data})
	}
	return out, nil
}

// unresolvedPlaceholder is shown in place of a value the lab has not produced (or that has no lab at all), as the
// participant's page does.
const unresolvedPlaceholder = "—"

// answerKind is the answer_kind of a subtask: the type of its answer, or state when the lab's state is what is checked.
func answerKind(task exerciseModel.Task) string {
	if answer := task.Check.Answer; answer != nil {
		return string(answer.Type)
	}
	return "state"
}

// placeholderResolver resolves the {{key}} variables of a description for one lab: the subnets and addresses of its
// network and the access links of its devices.
//
// DIVERGENCE RISK: the participant's page fills these values in the browser, not on the server, so this is a second
// implementation of the same rules. The source of truth is event-frontend
// src/components/event/challenges/descriptionValues.ts (descriptionValues, resolveOne, relativeIp, linkUrl); its tests are
// mirrored in testdata/placeholder_values.json, which the golden test of this file compares against. A change of a rule
// there must be made here too (the one shared source is on the backlog). Known differences: this side formats the
// description to plain text (the page renders rich text), and a lab is "current" here when it is ready (the page also
// compares the lab's lifecycle revision).
func placeholderResolver(placeholders []exerciseModel.Placeholder, status exerciseModel.LabDeployStatus) func(string) (string, bool) {
	values := placeholderValues(placeholders, status)
	return func(key string) (string, bool) {
		if value, ok := values[key]; ok {
			return value, true
		}
		return unresolvedPlaceholder, true
	}
}

// placeholderValues is descriptionValues of the page: the value of every placeholder that has a key.
func placeholderValues(placeholders []exerciseModel.Placeholder, status exerciseModel.LabDeployStatus) map[string]string {
	values := make(map[string]string, len(placeholders))
	for _, p := range placeholders {
		if p.Key == "" {
			continue
		}
		static := p.Kind == exerciseModel.PlaceholderIP && p.IPReference == "static"
		if !status.Ready && !static {
			values[p.Key] = unresolvedPlaceholder
			continue
		}
		if p.Kind == exerciseModel.PlaceholderExternalLink {
			values[p.Key] = firstNonEmpty(accessURL(p.DeviceName, status), unresolvedPlaceholder)
			continue
		}
		address := resolveAddress(p, status)
		if p.Kind == exerciseModel.PlaceholderIP && p.AsLink && address != "" {
			scheme := p.Scheme
			if scheme == "" {
				scheme = "http"
			}
			link := scheme + "://" + address
			if p.Port > 0 {
				link += ":" + strconv.Itoa(p.Port)
			}
			values[p.Key] = link + p.Path
			continue
		}
		values[p.Key] = firstNonEmpty(address, unresolvedPlaceholder)
	}
	return values
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// accessURL is the url of the first access entry of the device that has one, whatever its kind.
func accessURL(device string, status exerciseModel.LabDeployStatus) string {
	for _, a := range status.Access {
		if a.Device == device && a.URL != "" {
			return a.URL
		}
	}
	return ""
}

// gatewayCIDR is the /24 of the lab's gateway of that name; empty until it has one.
func gatewayCIDR(status exerciseModel.LabDeployStatus, name string) string {
	for _, g := range status.Gateways {
		if g.Name == name {
			return g.CIDR
		}
	}
	return ""
}

// resolveAddress is the text of a subnet or an IP placeholder; empty when the lab cannot give it.
func resolveAddress(p exerciseModel.Placeholder, status exerciseModel.LabDeployStatus) string {
	switch p.Kind {
	case exerciseModel.PlaceholderGatewaySubnet:
		return gatewayCIDR(status, p.Gateway)
	case exerciseModel.PlaceholderIP:
		// A mask is shown on a plain address only, never on a link or a static address.
		mask := p.ShowMask && !p.AsLink
		switch p.IPReference {
		case "gateway":
			return relativeIP(gatewayCIDR(status, p.Gateway), p.LastOctet, mask)
		case "static":
			if p.Octets1to3 == "" {
				return ""
			}
			return p.Octets1to3 + "." + strconv.Itoa(p.LastOctet)
		}
	}
	return ""
}

// relativeIP is the address with the last octet in the network of a CIDR (10.8.3.0/24, 7 gives 10.8.3.7), with the mask of
// the CIDR when asked.
func relativeIP(cidr string, lastOctet int, showMask bool) string {
	address, mask, ok := strings.Cut(cidr, "/")
	parts := strings.Split(address, ".")
	if !ok || mask == "" || len(parts) != 4 {
		return ""
	}
	out := strings.Join(parts[:3], ".") + "." + strconv.Itoa(lastOctet)
	if showMask {
		out += "/" + mask
	}
	return out
}

// renderDescription turns the editor document of a description into the text a participant reads: blocks are separated
// by a blank line, a line break stays one, a list item starts with "- ", a link keeps its address, and a variable is its
// resolved value (or {{key}} when the lab cannot give it).
func renderDescription(document json.RawMessage, resolve func(string) (string, bool)) string {
	if len(document) == 0 {
		return ""
	}
	var root map[string]any
	if json.Unmarshal(document, &root) != nil {
		return ""
	}
	node, _ := root["root"].(map[string]any)
	if node == nil {
		node = root
	}
	return strings.TrimSpace(renderNode(node, resolve))
}

func renderNode(node map[string]any, resolve func(string) (string, bool)) string {
	switch node["type"] {
	case "text":
		text, _ := node["text"].(string)
		return text
	case "linebreak":
		return "\n"
	case "variable":
		key, _ := node["varName"].(string)
		if value, ok := resolve(key); ok {
			return value
		}
		return "{{" + key + "}}"
	}
	children, _ := node["children"].([]any)
	parts := make([]string, 0, len(children))
	for _, child := range children {
		if c, ok := child.(map[string]any); ok {
			parts = append(parts, renderNode(c, resolve))
		}
	}
	switch node["type"] {
	case "root":
		return strings.Join(nonEmpty(parts), "\n\n")
	case "list":
		return strings.Join(nonEmpty(parts), "\n")
	case "listitem":
		return "- " + strings.Join(parts, "")
	case "link":
		text := strings.Join(parts, "")
		if url, _ := node["url"].(string); url != "" && !strings.Contains(text, url) {
			return text + " (" + url + ")"
		}
		return text
	}
	return strings.Join(parts, "")
}

func nonEmpty(parts []string) []string {
	out := parts[:0]
	for _, p := range parts {
		if strings.TrimSpace(p) != "" {
			out = append(out, p)
		}
	}
	return out
}
