package server

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/abjelosevic88/pocket-homelab-audiobooks/internal/store"
)

// Without CORE_URL the settings say there is no Core, so the UI can grey out
// "Pocket Core" instead of offering it.
func TestSettingsCoreFlagsWithoutCore(t *testing.T) {
	cfg := testConfig(t.TempDir())
	st := openTestStore(t, cfg)
	s := New(cfg, st, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	ctx := context.Background()
	u := &store.User{Email: "ana@example.com", Name: "Ana", Role: "admin", PasswordHash: "x"}
	if err := st.CreateUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	set, _ := st.GetSettings(ctx, u.ID)
	m := s.publicSettings(ctx, u, set)
	if m["core_configured"] != false || m["core_registered"] != false {
		t.Fatalf("core flags = %v / %v, want false / false", m["core_configured"], m["core_registered"])
	}
}
