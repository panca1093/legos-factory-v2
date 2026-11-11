package app

import "testing"

func TestLoadConfigIgnoresRemovedEnvVars(t *testing.T) {
	t.Setenv("FACTORY_RUN_TIMEOUT", "99")
	t.Setenv("FACTORY_SANDBOX_MODE", "nonsense")
	if _, err := LoadConfig(); err != nil {
		t.Fatalf("an old .env must still start: %v", err)
	}
}
