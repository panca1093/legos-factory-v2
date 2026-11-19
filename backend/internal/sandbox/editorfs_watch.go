package sandbox

import (
	"errors"

	"legos-factory/backend/internal/model"
)

func (EditorFS) Watch(root string) (model.Watcher, error) {
	return nil, errors.New("watch not built")
}
