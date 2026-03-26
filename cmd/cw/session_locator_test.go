package main

import "testing"

func TestParseSessionLocator(t *testing.T) {
	t.Parallel()

	t.Run("local name", func(t *testing.T) {
		loc, err := parseSessionLocator("coder")
		if err != nil {
			t.Fatalf("parseSessionLocator: %v", err)
		}
		if loc.Node != "" || loc.Name != "coder" || loc.ID != nil {
			t.Fatalf("unexpected locator: %+v", loc)
		}
	})

	t.Run("local id", func(t *testing.T) {
		loc, err := parseSessionLocator("17")
		if err != nil {
			t.Fatalf("parseSessionLocator: %v", err)
		}
		if loc.Node != "" || loc.ID == nil || *loc.ID != 17 {
			t.Fatalf("unexpected locator: %+v", loc)
		}
	})

	t.Run("remote name", func(t *testing.T) {
		loc, err := parseSessionLocator("dev-2:coder")
		if err != nil {
			t.Fatalf("parseSessionLocator: %v", err)
		}
		if loc.Node != "dev-2" || loc.Name != "coder" || loc.ID != nil {
			t.Fatalf("unexpected locator: %+v", loc)
		}
	})

	t.Run("remote id", func(t *testing.T) {
		loc, err := parseSessionLocator("dev-2:17")
		if err != nil {
			t.Fatalf("parseSessionLocator: %v", err)
		}
		if loc.Node != "dev-2" || loc.ID == nil || *loc.ID != 17 {
			t.Fatalf("unexpected locator: %+v", loc)
		}
	})

	t.Run("invalid empty target", func(t *testing.T) {
		if _, err := parseSessionLocator("dev-2:"); err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("invalid too many colons", func(t *testing.T) {
		if _, err := parseSessionLocator("a:b:c"); err == nil {
			t.Fatal("expected error")
		}
	})
}
