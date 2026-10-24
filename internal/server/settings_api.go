package server

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/abjelosevic88/pocket-homelab-audiobooks/internal/notify"
	"github.com/abjelosevic88/pocket-homelab-audiobooks/internal/store"
)

// ---- settings ----

func mask(s string) string {
	if s == "" {
		return ""
	}
	if len(s) <= 8 {
		return "••••"
	}
	return s[:4] + "••••" + s[len(s)-4:]
}

// publicSettings is what one person sees: their settings with secrets masked.
func (s *Server) publicSettings(ctx context.Context, u *store.User, st store.Settings) map[string]any {
	return map[string]any{
		"core_configured": s.core.Configured(), "core_registered": s.coreStatus()["registered"],
		"title": st.Title, "date_format": st.DateFormat, "time_format": st.TimeFormat,
		"speed": st.Speed, "skip_back": st.SkipBack, "skip_forward": st.SkipForward, "rewind_on_resume": st.RewindOnResume, "play_next": st.PlayNext,
		"hidden_libraries": st.HiddenLibraries, "daily_goal": st.DailyGoal,
		"notify_url": st.NotifyURL, "notify_format": st.NotifyFormat, "notify_finished": st.NotifyFinished, "notify_weekly": st.NotifyWeekly, "notify_configured": st.NotifyURL != "",
		"setup_complete": st.SetupComplete, "base_url": s.cfg.BaseURL, "abs_url": s.cfg.ABSURL != "", "abs_public_url": s.cfg.ABSPublicURL,
	}
}

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	st, err := s.store.GetSettings(r.Context(), actorID(r.Context()))
	if err != nil {
		s.fail(w, err, "settings")
		return
	}
	writeJSON(w, http.StatusOK, s.publicSettings(r.Context(), userFrom(r.Context()), st))
}

// handlePutSettings saves the caller's personal settings. The box-wide title
// is only taken from an admin and silently ignored for everyone else.
func (s *Server) handlePutSettings(w http.ResponseWriter, r *http.Request) {
	me := userFrom(r.Context())
	isAdmin := me.Role == "admin"
	cur, err := s.store.GetSettings(r.Context(), me.ID)
	if err != nil {
		s.fail(w, err, "settings")
		return
	}
	var in store.Settings
	keys, err := decodeKeys(r, &in)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if keys["title"] && isAdmin {
		cur.Title = strings.TrimSpace(in.Title)
		if cur.Title == "" {
			cur.Title = store.DefaultTitle
		}
	}
	oneOf := func(k string, dst *string, v string, allowed ...string) {
		if !keys[k] {
			return
		}
		for _, a := range allowed {
			if v == a {
				*dst = v
				return
			}
		}
	}
	intIn := func(k string, dst *int, v, lo, hi int) {
		if keys[k] && v >= lo && v <= hi {
			*dst = v
		}
	}
	text := func(k string, dst *string, v string) {
		if keys[k] {
			*dst = strings.TrimSpace(v)
		}
	}
	boolIn := func(k string, dst *bool, v bool) {
		if keys[k] {
			*dst = v
		}
	}
	text("date_format", &cur.DateFormat, in.DateFormat)
	text("time_format", &cur.TimeFormat, in.TimeFormat)
	if keys["speed"] && in.Speed >= 0.5 && in.Speed <= 3.5 {
		cur.Speed = float64(int(in.Speed*100+0.5)) / 100
	}
	intIn("skip_back", &cur.SkipBack, in.SkipBack, 5, 120)
	intIn("skip_forward", &cur.SkipForward, in.SkipForward, 5, 300)
	intIn("rewind_on_resume", &cur.RewindOnResume, in.RewindOnResume, 0, 60)
	boolIn("play_next", &cur.PlayNext, in.PlayNext)
	text("hidden_libraries", &cur.HiddenLibraries, in.HiddenLibraries)
	intIn("daily_goal", &cur.DailyGoal, in.DailyGoal, 0, 600)
	text("notify_url", &cur.NotifyURL, in.NotifyURL)
	if keys["notify_format"] && in.NotifyFormat != "" {
		oneOf("notify_format", &cur.NotifyFormat, in.NotifyFormat, "apprise", "ntfy", "json")
	}
	boolIn("notify_finished", &cur.NotifyFinished, in.NotifyFinished)
	boolIn("notify_weekly", &cur.NotifyWeekly, in.NotifyWeekly)
	if err := s.store.SaveUserSettings(r.Context(), me.ID, cur); err != nil {
		s.fail(w, err, "save settings")
		return
	}
	if isAdmin && keys["title"] {
		if err := s.store.SaveBoxSettings(r.Context(), cur); err != nil {
			s.fail(w, err, "save settings")
			return
		}
	}
	writeJSON(w, http.StatusOK, s.publicSettings(r.Context(), me, cur))
}

// handleIntegrationTest checks a connection with the caller's own saved
// settings (or the URL/key typed into the form).
func (s *Server) handleIntegrationTest(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Which  string `json:"which"`
		URL    string `json:"url"`
		Key    string `json:"key"`
		Format string `json:"format"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	u := userFrom(r.Context())
	st, _ := s.store.GetSettings(r.Context(), u.ID)
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	urlOr := func(saved string) string {
		if strings.TrimSpace(in.URL) == "" {
			return saved
		}
		return strings.TrimRight(strings.TrimSpace(in.URL), "/")
	}
	var msg string
	var err error
	switch in.Which {
	case "notify":
		f := in.Format
		if f == "" {
			f = st.NotifyFormat
		}
		n := notify.New(urlOr(st.NotifyURL), f)
		err = n.Send(ctx, notify.Message{Title: "Pocket Audiobooks", Body: "Notifications work. Finished books and your weekly recap will come here.", Priority: "info", Tags: []string{"headphones"}})
		msg = "Test message sent via " + n.Format
	default:
		writeErr(w, http.StatusBadRequest, "unknown integration")
		return
	}
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": msg})
}

func (s *Server) handleSystemInfo(w http.ResponseWriter, r *http.Request) {
	var size int64
	if fi, err := os.Stat(s.cfg.DBPath); err == nil {
		size = fi.Size()
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"version": Version, "go": runtime.Version(), "os": runtime.GOOS, "arch": runtime.GOARCH,
		"core":      s.coreStatus(),
		"auth_mode": s.cfg.AuthMode, "data_dir": s.cfg.DataDir, "db_path": s.cfg.DBPath, "db_bytes": size,
		"uptime": time.Since(s.started).Round(time.Second).String(), "timezone": time.Now().Location().String(),
		"counts": s.store.CountsFor(r.Context(), actorID(r.Context())), "abs_url": s.cfg.ABSURL,
	})
}

// handleBackup downloads the whole database, so it is a box operation for an
// admin only (everyone can export their own data as JSON).
func (s *Server) handleBackup(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	dir := filepath.Join(s.cfg.DataDir, "backups")
	name := fmt.Sprintf("pocket-audiobooks-%s.db", time.Now().Format("20060102-150405"))
	dst := filepath.Join(dir, name)
	if err := s.store.Backup(r.Context(), dst); err != nil {
		s.fail(w, err, "backup")
		return
	}
	w.Header().Set("Content-Type", "application/vnd.sqlite3")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	http.ServeFile(w, r, dst)
}

// exportDoc is the JSON export of one person's own data: listening history,
// finished books and up next (the books themselves live in Audiobookshelf).
type exportDoc struct {
	App      string         `json:"app"`
	Version  string         `json:"version"`
	Exported string         `json:"exported_at"`
	Listens  []store.Listen `json:"listens"`
	Finishes []store.Finish `json:"finished"`
	UpNext   []store.UpNext `json:"up_next"`
}

func (s *Server) handleExportJSON(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	uid := actorID(ctx)
	doc := exportDoc{App: "pocket-audiobooks", Version: Version, Exported: store.Now()}
	doc.Listens, _, _ = s.store.Listens(ctx, uid, store.ListenFilter{Limit: -1})
	doc.Finishes, _ = s.store.Finishes(ctx, uid, "", "")
	doc.UpNext, _ = s.store.UpNextList(ctx, uid)
	w.Header().Set("Content-Disposition", `attachment; filename="pocket-audiobooks-export.json"`)
	writeJSON(w, http.StatusOK, doc)
}

// handleImportJSON adds an export to the caller's own data (rows already there are kept).
func (s *Server) handleImportJSON(w http.ResponseWriter, r *http.Request) {
	var doc exportDoc
	if err := decode(r, &doc); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	ctx := r.Context()
	uid := actorID(ctx)
	counts := map[string]int{}
	for _, l := range doc.Listens {
		l.ID = 0
		if l.ItemID == "" || l.StartedAt == "" {
			continue
		}
		if l.ABSID == "" {
			l.Source = "import"
		}
		if ok, err := s.store.UpsertListen(ctx, uid, &l); err == nil && ok {
			counts["listens"]++
		}
	}
	for _, f := range doc.Finishes {
		f.ID = 0
		if f.ItemID == "" || f.FinishedAt == "" {
			continue
		}
		if ok, err := s.store.AddFinish(ctx, uid, &f); err == nil && ok {
			counts["finished"]++
		}
	}
	for _, x := range doc.UpNext {
		if x.ItemID != "" && s.store.AddUpNext(ctx, uid, &x, false) == nil {
			counts["up_next"]++
		}
	}
	writeJSON(w, http.StatusOK, counts)
}
