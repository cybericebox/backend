// Package mediaencryptJob wires the pass that encrypts older plain objects into River.
package mediaencryptJob

import (
	"context"
	"time"

	"github.com/riverqueue/river"

	jobsModel "github.com/cybericebox/daemon/internal/model/jobs"
	"github.com/cybericebox/daemon/pkg/secret"
)

type IUseCase interface {
	EncryptLegacyBlobs(ctx context.Context) (secret.Progress, error)
}

type mediaencryptWorker struct {
	river.WorkerDefaults[jobsModel.MediaEncryptArgs]
	uc IUseCase
}

// Timeout: a pass over a large bucket takes long; the next run continues where this one stopped (what is encrypted drops out
// of the list).
func (w *mediaencryptWorker) Timeout(*river.Job[jobsModel.MediaEncryptArgs]) time.Duration {
	return 6 * time.Hour
}

func (w *mediaencryptWorker) Work(ctx context.Context, _ *river.Job[jobsModel.MediaEncryptArgs]) error {
	_, err := w.uc.EncryptLegacyBlobs(ctx)
	return err
}

// NewWorker builds this job's River worker from its narrow use-case port.
func NewWorker(uc IUseCase) *mediaencryptWorker {
	return &mediaencryptWorker{uc: uc}
}
