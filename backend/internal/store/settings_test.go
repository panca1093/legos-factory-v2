package store

import (
	"context"
	"testing"

	"legos-factory/backend/internal/model"
)

func TestSettingsPatchRollsBackWhenAWriteFails(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	_, err := db.ExecContext(ctx, `CREATE TRIGGER reject_second_setting BEFORE INSERT ON settings_kv
		WHEN EXISTS (SELECT 1 FROM settings_kv)
		BEGIN SELECT RAISE(ABORT, 'test write failure'); END`)
	if err != nil {
		t.Fatal(err)
	}
	repo := &SettingsRepo{DB: db}
	architect, chat := "test/architect", "test/chat"
	if err := repo.Patch(ctx, model.SettingsPatch{ArchitectModel: &architect, ChatModel: &chat}); err == nil {
		t.Fatal("expected PATCH to fail")
	}
	got, err := repo.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.ArchitectModel != "" || got.ChatModel != "" {
		t.Fatalf("failed PATCH persisted settings: %+v", got)
	}
}
