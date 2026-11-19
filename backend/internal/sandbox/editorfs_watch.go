package sandbox

import "legos-factory/backend/internal/model"

func (EditorFS) Watch(root string) (model.Watcher, error) { return NewWatcher(root) }
