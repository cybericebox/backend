package event

import (
	"bytes"
	"context"
	"io"
	"path"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gofrs/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/cybericebox/backend/internal/delivery/repository/eventAnswerFileRepo"
	"github.com/cybericebox/backend/internal/delivery/repository/eventFormRepo"
	repositoryTools "github.com/cybericebox/backend/internal/delivery/repository/tools"
	"github.com/cybericebox/backend/internal/model"
	eventModel "github.com/cybericebox/backend/internal/model/event"
	eventFormModel "github.com/cybericebox/backend/internal/model/eventForm"
	participantModel "github.com/cybericebox/backend/internal/model/participant"
)

const maxAnswerFileNameLength = 200

// AnswerFileLimits cap what one person's participant uploads keep waiting for an answer (ANSWER_FILE_PENDING_PER_FIELD,
// ANSWER_FILE_MAX_PENDING_BYTES_PER_EVENT, ANSWER_FILE_MAX_PENDING_BYTES_PER_USER): the uploads of one question, their
// bytes in one event and in every event together (so events open for registration do not multiply the budget).
// Attached files are not counted: the form bounds them, and an answer that replaces a file releases the old one.
// Unused uploads expire under retention (UnusedAnswerFile, 24 h).
type AnswerFileLimits struct {
	PendingPerField      int64
	PendingBytesPerEvent int64
	PendingBytesPerUser  int64
}

// DefaultAnswerFileLimits are the env defaults: 5 waiting uploads per question, 256 MiB waiting per person and event,
// 512 MiB waiting per person in all events.
func DefaultAnswerFileLimits() AnswerFileLimits {
	return AnswerFileLimits{PendingPerField: 5, PendingBytesPerEvent: 256 << 20, PendingBytesPerUser: 512 << 20}
}

var answerFileLimits = DefaultAnswerFileLimits()

func (l AnswerFileLimits) caps() eventAnswerFileRepo.Caps {
	return eventAnswerFileRepo.Caps{PendingPerField: l.PendingPerField, EventPendingBytes: l.PendingBytesPerEvent, UserPendingBytes: l.PendingBytesPerUser}
}

func ConfigureAnswerFileLimits(l AnswerFileLimits) { answerFileLimits = l }

// AnswerFileView is an uploaded answer file as the client stores it in the
// answer and shows it.
type AnswerFileView struct {
	ID          uuid.UUID
	Name        string
	Size        int64
	ContentType string
}

func toAnswerFileView(f eventFormModel.AnswerFile) AnswerFileView {
	return AnswerFileView{ID: f.FileID, Name: f.Name, Size: f.SizeBytes, ContentType: f.ContentType}
}

// answerFileName keeps the base name of the client file name, readable and
// bounded; the stored name is only ever sent back in a quoted header or JSON.
func answerFileName(name string) string {
	name = strings.TrimSpace(path.Base(strings.ReplaceAll(name, "\\", "/")))
	if name == "" || name == "." || name == "/" || !utf8.ValidString(name) {
		return "file"
	}
	if runes := []rune(name); len(runes) > maxAnswerFileNameLength {
		name = string(runes[len(runes)-maxAnswerFileNameLength:])
	}
	return name
}

// answerForm returns the form a file question of the scope belongs to; found
// is false when the event has no such form.
func (u *EventUseCase) answerForm(ctx context.Context, eventID uuid.UUID, scope eventFormModel.AnswerScope) (eventFormModel.Form, bool, error) {
	var (
		form eventFormModel.Form
		err  error
	)
	switch scope {
	case eventFormModel.AnswerScopeParticipant:
		var version eventFormRepo.Version
		version, err = u.forms.Latest(ctx, eventID)
		form = version.Form
	case eventFormModel.AnswerScopeTeam:
		form, err = u.teams.GetFieldConfig(ctx, eventID)
	default:
		return eventFormModel.Form{}, false, nil
	}
	if err != nil {
		if repositoryTools.IsObjectNotFoundError(err) || err == pgx.ErrNoRows {
			return eventFormModel.Form{}, false, nil
		}
		return eventFormModel.Form{}, false, model.ErrPlatform.WithError(err).WithMessage("Failed to get answer form").Err()
	}
	return form, true, nil
}

// UploadAnswerFile stores a file for a "File" question of the event's
// participant form or team fields. The file must match the question's formats
// by content and fit its size limit. It stays with the uploader until an
// answer that references it is saved; unused uploads expire under retention.
func (u *EventUseCase) UploadAnswerFile(ctx context.Context, eventID, actorID uuid.UUID, scope eventFormModel.AnswerScope, fieldKey, name string, r io.Reader) (AnswerFileView, error) {
	return u.uploadAnswerFile(ctx, eventID, actorID, scope, fieldKey, name, r, false)
}

// uploadAnswerFile stores the file; capped applies the participant caps (AnswerFileLimits) to the uploader.
func (u *EventUseCase) uploadAnswerFile(ctx context.Context, eventID, actorID uuid.UUID, scope eventFormModel.AnswerScope, fieldKey, name string, r io.Reader, capped bool) (AnswerFileView, error) {
	form, found, err := u.answerForm(ctx, eventID, scope)
	if err != nil {
		return AnswerFileView{}, err
	}
	field, ok := form.FileFields()[fieldKey]
	if !found || !ok || !form.Enabled {
		return AnswerFileView{}, eventModel.ErrAnswerFileFieldInvalid.Err()
	}
	budget := int64(-1)
	if capped {
		usage, usageErr := u.answerFiles.Usage(ctx, eventID, actorID, scope, fieldKey)
		if usageErr != nil {
			return AnswerFileView{}, model.ErrPlatform.WithError(usageErr).WithMessage("Failed to count answer files").Err()
		}
		var refused bool
		if budget, refused = answerFileLimits.caps().Budget(usage); refused {
			return AnswerFileView{}, eventModel.ErrAnswerFilePendingLimit.Err()
		}
	}
	// Never read more than the question allows or, for a capped upload, than the budget leaves (a byte more tells
	// which one it went past).
	limit := eventFormModel.FileLimitBytes(field)
	readLimit := limit
	if budget >= 0 && budget < limit {
		readLimit = budget
	}
	data, err := io.ReadAll(io.LimitReader(r, readLimit+1))
	if err != nil {
		return AnswerFileView{}, model.ErrPlatform.WithError(err).WithMessage("Failed to read answer file").Err()
	}
	switch size := int64(len(data)); {
	case size > limit:
		return AnswerFileView{}, eventModel.ErrAnswerFileTooLarge.Err()
	case size > readLimit:
		return AnswerFileView{}, eventModel.ErrAnswerFileQuotaExceeded.Err()
	}
	name = answerFileName(name)
	kind, contentType, err := eventFormModel.DetectFileKind(name, data)
	if err != nil || !eventFormModel.AllowsFile(field, kind) {
		return AnswerFileView{}, eventModel.ErrAnswerFileTypeInvalid.Err()
	}
	file, err := u.brandMedia.UploadFile(ctx, name, contentType, bytes.NewReader(data), actorID)
	if err != nil {
		return AnswerFileView{}, err
	}
	answerFile := eventFormModel.NewAnswerFile(file.ID, eventID, scope, fieldKey, name, file.SizeBytes, contentType, actorID, time.Now().UTC())
	if capped {
		return u.createCappedAnswerFile(ctx, answerFile)
	}
	if err = u.answerFiles.Create(ctx, answerFile); err != nil {
		return AnswerFileView{}, model.ErrPlatform.WithError(err).WithMessage("Failed to record answer file").Err()
	}
	return toAnswerFileView(answerFile), nil
}

// createCappedAnswerFile records a participant upload only if it still fits the caps when it is written: the
// check before the upload can race with a parallel upload of the same person, so the row is written under their
// lock against their usage read again. A refused upload's stored file is discarded at once.
func (u *EventUseCase) createCappedAnswerFile(ctx context.Context, f eventFormModel.AnswerFile) (AnswerFileView, error) {
	refuse := func(err error) (AnswerFileView, error) {
		if discardErr := u.brandMedia.DiscardFile(ctx, f.FileID, f.UploadedBy); discardErr != nil {
			return AnswerFileView{}, discardErr
		}
		return AnswerFileView{}, err
	}
	if u.uow == nil {
		return refuse(model.ErrPlatform.WithMessage("Event transaction is not configured").Err())
	}
	txCtx, txRepo, unit, err := u.uow.UnitOfWork(ctx)
	if err != nil {
		return refuse(err)
	}
	defer func() { _ = unit.Restore() }()
	caps := answerFileLimits.caps()
	usage, stored, err := eventAnswerFileRepo.New(txRepo).CreateWithinCaps(txCtx, f, caps)
	if err != nil {
		return refuse(model.ErrPlatform.WithError(err).WithMessage("Failed to record answer file").Err())
	}
	if !stored {
		if _, refused := caps.Budget(usage); refused {
			return refuse(eventModel.ErrAnswerFilePendingLimit.Err())
		}
		return refuse(eventModel.ErrAnswerFileQuotaExceeded.Err())
	}
	if err = unit.Save(); err != nil {
		return refuse(model.ErrPlatform.WithError(err).WithMessage("Failed to record answer file").Err())
	}
	return toAnswerFileView(f), nil
}

// UploadOwnAnswerFile is UploadAnswerFile for a participant (the event site), under the rules of saving the
// answers: the participant form is part of applying, so it refuses only a rejected person (as SubmitParticipantForm);
// team fields are filled by an approved participant only (creating a team, or its captain editing them; the team
// itself is checked when the answer that references the file is saved). After the event's effective finish the
// answers are frozen, so nothing is uploaded; someone who has not applied yet uploads only while they may apply
// (the event is published and its join period open). One person's uploads are capped (AnswerFileLimits).
func (u *EventUseCase) UploadOwnAnswerFile(ctx context.Context, eventID, userID uuid.UUID, scope eventFormModel.AnswerScope, fieldKey, name string, r io.Reader) (AnswerFileView, error) {
	p, err := u.participants.Get(ctx, eventID, userID)
	if err != nil && !repositoryTools.IsObjectNotFoundError(err) && err != pgx.ErrNoRows {
		return AnswerFileView{}, model.ErrPlatform.WithError(err).WithMessage("Failed to get event participant").Err()
	}
	found := err == nil
	switch {
	case scope == eventFormModel.AnswerScopeTeam && !found:
		return AnswerFileView{}, participantModel.ErrParticipantNotFound.Err()
	case scope == eventFormModel.AnswerScopeTeam && p.Status != participantModel.StatusApproved:
		return AnswerFileView{}, participantModel.ErrParticipantNotApproved.Err()
	case found && p.Status == participantModel.StatusRejected:
		return AnswerFileView{}, participantModel.ErrParticipantAccessForbidden.Err()
	}
	e, err := u.events.GetByID(ctx, eventID)
	if err != nil {
		if repositoryTools.IsObjectNotFoundError(err) {
			return AnswerFileView{}, eventModel.ErrEventNotFound.Err()
		}
		return AnswerFileView{}, model.ErrPlatform.WithError(err).WithMessage("Failed to get event").Err()
	}
	now := time.Now()
	if !participantModel.AnswersEditable(e.Lifecycle.EffectiveFinishAt(), now) {
		return AnswerFileView{}, participantModel.ErrParticipantFieldsLocked.Err()
	}
	if !found && !e.Lifecycle.RegistrationOpen(now) {
		return AnswerFileView{}, participantModel.ErrRegistrationNotOpen.Err()
	}
	return u.uploadAnswerFile(ctx, eventID, userID, scope, fieldKey, name, r, true)
}

// boundAnswers are answers whose file references were checked and replaced
// with the stored file metadata, ready to save and then attach.
type boundAnswers struct {
	values   map[string]any
	files    []uuid.UUID
	hasFiles bool
	// missing counts the required team fields the values leave unfilled
	// (only when the organizer asked every team for them).
	missing int32
}

// bindAnswerFiles checks every file answer: the file was uploaded for this
// event, scope and question, by the actor (uuid.Nil is event staff) unless
// the owner's saved answer already holds it. Forms without file questions
// pass through untouched.
func bindAnswerFiles(ctx context.Context, repo *eventAnswerFileRepo.Repository, eventID uuid.UUID, scope eventFormModel.AnswerScope, actor, owner uuid.UUID, form eventFormModel.Form, answers map[string]any) (boundAnswers, error) {
	fields := form.FileFields()
	if len(fields) == 0 {
		return boundAnswers{values: answers}, nil
	}
	ids := make(map[string]uuid.UUID)
	valid := true
	for key := range fields {
		if value, present := answers[key]; present && value != nil && value != "" {
			id, ok := eventFormModel.AnswerFileID(value)
			valid = valid && ok
			ids[key] = id
		}
	}
	bound := boundAnswers{values: make(map[string]any, len(answers)), files: make([]uuid.UUID, 0, len(ids)), hasFiles: true}
	for key, value := range answers {
		bound.values[key] = value
	}
	if len(ids) == 0 {
		return bound, nil
	}
	var rows map[uuid.UUID]eventFormModel.AnswerFile
	if valid {
		list := make([]uuid.UUID, 0, len(ids))
		for _, id := range ids {
			list = append(list, id)
		}
		var err error
		if rows, err = repo.List(ctx, list); err != nil {
			return boundAnswers{}, model.ErrPlatform.WithError(err).WithMessage("Failed to get answer files").Err()
		}
	}
	for key, id := range ids {
		row, found := rows[id]
		if !found || !row.CanAnswer(eventID, scope, key, actor, owner) {
			valid = false
			break
		}
		bound.values[key] = row.Value()
		bound.files = append(bound.files, id)
	}
	if !valid {
		return boundAnswers{}, eventModel.ErrAnswerFileUnavailable.Err()
	}
	return bound, nil
}

// attach gives the saved answer's files to the owner and releases the ones
// the owner's answer no longer references.
func (b boundAnswers) attach(ctx context.Context, repo *eventAnswerFileRepo.Repository, eventID uuid.UUID, scope eventFormModel.AnswerScope, owner uuid.UUID) error {
	if !b.hasFiles {
		return nil
	}
	if err := repo.Attach(ctx, eventID, scope, owner, b.files, time.Now().UTC()); err != nil {
		return model.ErrPlatform.WithError(err).WithMessage("Failed to attach answer files").Err()
	}
	return nil
}

func (u *EventUseCase) streamAnswerFile(ctx context.Context, file eventFormModel.AnswerFile) (io.ReadCloser, AnswerFileView, error) {
	rc, _, err := u.brandMedia.StreamFile(ctx, file.FileID)
	if err != nil {
		return nil, AnswerFileView{}, err
	}
	return rc, toAnswerFileView(file), nil
}

func (u *EventUseCase) eventAnswerFile(ctx context.Context, eventID, fileID uuid.UUID) (eventFormModel.AnswerFile, error) {
	file, err := u.answerFiles.Get(ctx, fileID)
	if err != nil {
		if repositoryTools.IsObjectNotFoundError(err) || err == pgx.ErrNoRows {
			return eventFormModel.AnswerFile{}, eventModel.ErrAnswerFileNotFound.Err()
		}
		return eventFormModel.AnswerFile{}, model.ErrPlatform.WithError(err).WithMessage("Failed to get answer file").Err()
	}
	if file.EventID != eventID {
		return eventFormModel.AnswerFile{}, eventModel.ErrAnswerFileNotFound.Err()
	}
	return file, nil
}

// StreamManagedAnswerFile serves any answer file of the event to its staff
// (the route checks read access to the event).
func (u *EventUseCase) StreamManagedAnswerFile(ctx context.Context, eventID, fileID uuid.UUID) (io.ReadCloser, AnswerFileView, error) {
	file, err := u.eventAnswerFile(ctx, eventID, fileID)
	if err != nil {
		return nil, AnswerFileView{}, err
	}
	return u.streamAnswerFile(ctx, file)
}

// StreamOwnAnswerFile serves a participant their pending upload, their own
// answer's file or a file of their team's answers.
func (u *EventUseCase) StreamOwnAnswerFile(ctx context.Context, eventID, userID, fileID uuid.UUID) (io.ReadCloser, AnswerFileView, error) {
	file, err := u.eventAnswerFile(ctx, eventID, fileID)
	if err != nil {
		return nil, AnswerFileView{}, err
	}
	var teamID *uuid.UUID
	if participant, getErr := u.participants.Get(ctx, eventID, userID); getErr == nil {
		teamID = participant.TeamID
	} else if !repositoryTools.IsObjectNotFoundError(getErr) {
		return nil, AnswerFileView{}, model.ErrPlatform.WithError(getErr).WithMessage("Failed to get event participant").Err()
	}
	if !file.VisibleTo(userID, teamID) {
		return nil, AnswerFileView{}, eventModel.ErrAnswerFileNotFound.Err()
	}
	return u.streamAnswerFile(ctx, file)
}
