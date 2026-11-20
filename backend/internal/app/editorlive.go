package app

import (
	"time"

	"context"
	"legos-factory/backend/internal/model"
	"legos-factory/backend/internal/store"
	"legos-factory/backend/internal/usecase"
)

// newEditorLive builds the live half of the editor on the files half and points its conflict attribution at it.
func newEditorLive(ed *usecase.Editor, runs *store.RunRepo) *usecase.EditorLive {
	live := &usecase.EditorLive{Folders: ed, FS: ed.FS, Runs: runs}
	attr := &usecase.Attributor{Runs: runs}
	ed.Actor = func(ctx context.Context, f *model.EditorFolder, _ string, at time.Time) *model.EditorActor {
		return attr.Actor(ctx, *f, at, false)
	}
	return live
}
