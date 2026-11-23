package eventself

import (
	"bytes"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/gofrs/uuid"

	"github.com/cybericebox/backend/internal/delivery/controller/http/middleware"
	"github.com/cybericebox/backend/internal/delivery/controller/http/response"
	eventModel "github.com/cybericebox/backend/internal/model/event"
	"github.com/cybericebox/backend/internal/model/rbac"
	eventUseCase "github.com/cybericebox/backend/internal/useCase/event"
)

func answerFileRequest(fileID string) *http.Request {
	request := httptest.NewRequest(http.MethodGet, "/api/events/self/answer-files/"+fileID, nil)
	request = request.WithContext(middleware.ContextWithEventTenant(request.Context(), middleware.EventTenant{EventID: uuid.Must(uuid.NewV7())}))
	return request.WithContext(rbac.ContextWithCurrentUserSession(request.Context(), rbac.Claims{UserID: uuid.Must(uuid.NewV7())}))
}

func TestDownloadAnswerFileIsAnAttachment(t *testing.T) {
	fileID := uuid.Must(uuid.NewV7())
	h := NewEventSelfAPIHandler(fakeUseCase{answerFile: func() (io.ReadCloser, eventUseCase.AnswerFileView, error) {
		return io.NopCloser(strings.NewReader("%PDF")), eventUseCase.AnswerFileView{ID: fileID, Name: "резюме \"final\".pdf", Size: 4, ContentType: "application/pdf"}, nil
	}}, nil)
	w := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(w)
	ctx.Request = answerFileRequest(fileID.String())
	ctx.Params = gin.Params{{Key: "fileID", Value: fileID.String()}}
	h.downloadAnswerFile(ctx)
	if w.Code != http.StatusOK || w.Body.String() != "%PDF" {
		t.Fatalf("download: %d %q", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Content-Disposition"); !strings.HasPrefix(got, "attachment;") {
		t.Fatalf("file must download as an attachment: %q", got)
	}
	if w.Header().Get("X-Content-Type-Options") != "nosniff" || w.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("download headers: %v", w.Header())
	}
}

func TestDownloadAnswerFileHidesForeignFiles(t *testing.T) {
	fileID := uuid.Must(uuid.NewV7())
	h := NewEventSelfAPIHandler(fakeUseCase{answerFile: func() (io.ReadCloser, eventUseCase.AnswerFileView, error) {
		return nil, eventUseCase.AnswerFileView{}, eventModel.ErrAnswerFileNotFound.Err()
	}}, nil)
	w := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(w)
	ctx.Request = answerFileRequest(fileID.String())
	ctx.Params = gin.Params{{Key: "fileID", Value: fileID.String()}}
	h.downloadAnswerFile(ctx)
	got, ok := ctx.Get(response.ErrorCtxKey)
	if !ok || !errors.Is(got.(error), eventModel.ErrAnswerFileNotFound.Err()) || w.Body.Len() != 0 {
		t.Fatalf("a file of someone else must look missing: %v %q", got, w.Body.String())
	}
}

// Uploads are rate limited per user, as the email preview is: each one is stored before an answer uses it.
func TestUploadAnswerFileIsRateLimitedPerUser(t *testing.T) {
	router := participantPagesRouter(NewEventSelfAPIHandler(fakeUseCase{}, allowAllProtection{}), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()))
	upload := func() int {
		var body bytes.Buffer
		form := multipart.NewWriter(&body)
		part, _ := form.CreateFormFile("file", "cv.pdf")
		_, _ = part.Write([]byte("%PDF-1.7\n"))
		_ = form.WriteField("scope", "participant")
		_ = form.WriteField("field", "cv")
		_ = form.Close()
		request := httptest.NewRequest(http.MethodPost, "/api/events/self/answer-files", &body)
		request.Header.Set("Content-Type", form.FormDataContentType())
		w := httptest.NewRecorder()
		router.ServeHTTP(w, request)
		return w.Code
	}
	for i := range answerFileUploadsPerMinute {
		if code := upload(); code != http.StatusOK {
			t.Fatalf("upload %d: status=%d", i+1, code)
		}
	}
	if code := upload(); code != http.StatusTooManyRequests {
		t.Fatalf("over the limit: status=%d want 429", code)
	}
}
