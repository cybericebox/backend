package exercise

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
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
		// AnswerKind is how the participant's answer is read: flag (ICE{...}) or text. A subtask whose check is a state
		// of the lab has no answer to give and no kind.
		AnswerKind string `json:"answer_kind,omitempty"`
	}
)

// RunMaterials builds the participant's view of the task of the owner's test lab: the texts with the placeholders resolved
// for that lab, and the attachments read from the file store under the archive size limits (one file, and all together).
func (u *ExerciseUseCase) RunMaterials(ctx context.Context, owner, deployID uuid.UUID) (Materials, error) {
	status, deploy, err := u.deployTestStatus(ctx, owner, deployID)
	if err != nil {
		return Materials{}, err
	}
	exerciseID, variant, _, err := u.loadVersionVariant(ctx, deploy.VersionID, deploy.VariantID)
	if err != nil {
		return Materials{}, err
	}
	exercise, err := u.exercises.GetByID(ctx, exerciseID)
	if err != nil {
		return Materials{}, model.ErrPlatform.WithError(err).WithMessage("Failed to get exercise").Err()
	}
	doc := materialsTask{Title: exercise.Name, Description: exercise.Description, Subtasks: make([]materialsSubtask, 0, len(variant.Tasks))}
	var refs []exerciseModel.AttachmentRef
	for _, task := range variant.Tasks {
		sub := materialsSubtask{Key: task.Key, Title: task.Name, Prompt: renderDescription(task.Description, placeholderResolver(task.Placeholders, status))}
		if answer := task.Check.Answer; answer != nil {
			sub.AnswerKind = string(answer.Type)
		}
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

// placeholderResolver resolves the {{key}} variables of a description for one lab: the subnets and addresses of its
// network and the access links of its devices. A placeholder the lab cannot give is left as {{key}}.
func placeholderResolver(placeholders []exerciseModel.Placeholder, status exerciseModel.LabDeployStatus) func(string) (string, bool) {
	byKey := make(map[string]exerciseModel.Placeholder, len(placeholders))
	for _, p := range placeholders {
		if p.Key != "" {
			byKey[p.Key] = p
		}
	}
	return func(key string) (string, bool) {
		p, ok := byKey[key]
		if !ok {
			return "", false
		}
		return resolvePlaceholder(p, status)
	}
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

func resolvePlaceholder(p exerciseModel.Placeholder, status exerciseModel.LabDeployStatus) (string, bool) {
	switch p.Kind {
	case exerciseModel.PlaceholderGatewaySubnet:
		cidr := gatewayCIDR(status, p.Gateway)
		return cidr, cidr != ""
	case exerciseModel.PlaceholderIP:
		return resolveIP(p, status)
	case exerciseModel.PlaceholderExternalLink:
		return resolveLink(p, status)
	}
	return "", false
}

// resolveIP builds the address of an IP placeholder in the fixed /24 of its network.
func resolveIP(p exerciseModel.Placeholder, status exerciseModel.LabDeployStatus) (string, bool) {
	var prefix string
	switch p.IPReference {
	case "gateway":
		prefix = octets(gatewayCIDR(status, p.Gateway))
	case "static":
		prefix = p.Octets1to3
	}
	address := prefix + "." + strconv.Itoa(p.LastOctet)
	if prefix == "" || net.ParseIP(address) == nil {
		return "", false
	}
	if p.AsLink {
		link := p.Scheme + "://" + address
		if p.Port > 0 {
			link += ":" + strconv.Itoa(p.Port)
		}
		return link + p.Path, true
	}
	if p.ShowMask {
		address += "/24"
	}
	return address, true
}

// octets are the first three octets of a network written as a CIDR ("10.8.3.0/24" gives "10.8.3").
func octets(cidr string) string {
	ip, _, err := net.ParseCIDR(cidr)
	if err != nil || ip.To4() == nil {
		return ""
	}
	v4 := ip.To4()
	return fmt.Sprintf("%d.%d.%d", v4[0], v4[1], v4[2])
}

// resolveLink is the access link an external.link placeholder points to: the url the laboratory gave the entry.
func resolveLink(p exerciseModel.Placeholder, status exerciseModel.LabDeployStatus) (string, bool) {
	kind := p.EffectiveAccess()
	for _, a := range status.Access {
		if a.Device != p.DeviceName || a.URL == "" {
			continue
		}
		if kind == exerciseModel.AccessHTTP && (a.Protocol == "" || strings.HasPrefix(a.Protocol, string(exerciseModel.AccessHTTP))) {
			return a.URL, true
		}
		if kind != exerciseModel.AccessHTTP && a.Protocol == string(kind) {
			return a.URL, true
		}
	}
	return "", false
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
