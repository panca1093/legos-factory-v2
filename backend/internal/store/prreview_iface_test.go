package store_test

import (
	"legos-factory/backend/internal/store"
	"legos-factory/backend/internal/usecase"
)

var _ usecase.ReviewStore = (*store.ReviewRepo)(nil)
