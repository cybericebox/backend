// Package draftkeygcJob wires the deletion of stale draft keys (unused for their lifetime) into River.
package draftkeygcJob

import (
	"context"

	"github.com/riverqueue/river"

	jobsModel "github.com/cybericebox/backend/internal/model/jobs"
)

type IUseCase interface{ CleanupStaleDraftKeys(context.Context) error }

type worker struct {
	river.WorkerDefaults[jobsModel.DraftKeyGCArgs]
	uc IUseCase
}

func (w *worker) Work(ctx context.Context, _ *river.Job[jobsModel.DraftKeyGCArgs]) error {
	return w.uc.CleanupStaleDraftKeys(ctx)
}

func NewWorker(uc IUseCase) *worker { return &worker{uc: uc} }
