package app

import (
	"strings"
	"testing"
)

func TestLoadConfigTypeSafeModel(t *testing.T) {
	t.Setenv("TYPESAFE_MODEL", "jev-latest")
	if _, err := LoadConfig(); err == nil || !strings.Contains(err.Error(), "jev-latest") {
		t.Fatalf("err = %v, want jev-latest rejection", err)
	}
	t.Setenv("TYPESAFE_MODEL", "Jev-Latest")
	if _, err := LoadConfig(); err == nil {
		t.Fatal("case-insensitive jev-latest must be rejected")
	}
	t.Setenv("TYPESAFE_MODEL", "jev-1.14.0")
	c, err := LoadConfig()
	if err != nil || c.TypeSafeModel != "jev-1.14.0" {
		t.Fatalf("pinned model: %v %+v", err, c)
	}
}
