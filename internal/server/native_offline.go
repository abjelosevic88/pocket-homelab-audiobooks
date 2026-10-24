package server

import (
	"encoding/json"
	"github.com/abjelosevic88/pocket-homelab-audiobooks/internal/library"
	"net/http"
)

func (s *Server) nativeOffline(w http.ResponseWriter, r *http.Request, uid int64, batch bool) {
	var sessions []library.OfflineSession
	if batch {
		var in struct {
			Sessions   []library.OfflineSession `json:"sessions"`
			DeviceInfo json.RawMessage          `json:"deviceInfo"`
		}
		if err := decode(r, &in); err != nil || len(in.Sessions) > 1000 {
			writeErr(w, 400, "invalid offline sessions")
			return
		}
		sessions = in.Sessions
		if len(in.DeviceInfo) > 0 {
			for i := range sessions {
				if err := json.Unmarshal(in.DeviceInfo, &sessions[i].DeviceInfo); err != nil {
					writeErr(w, 400, "invalid device information")
					return
				}
			}
		}
	} else {
		var in library.OfflineSession
		if err := decode(r, &in); err != nil {
			writeErr(w, 400, "invalid offline session")
			return
		}
		sessions = []library.OfflineSession{in}
	}
	results := []any{}
	for _, in := range sessions {
		out, err := s.catalog().SyncOffline(r.Context(), uid, in)
		if !batch && err != nil {
			s.nativeError(w, err)
			return
		}
		entry := nativeObject{"id": in.ID, "success": err == nil, "duplicate": out.Duplicate, "progressApplied": out.ProgressApplied}
		if err != nil {
			entry["error"] = "session unavailable or invalid"
		} else if out.FinishedNow {
			s.nativeNotifyFinished(r.Context(), uid, in.ItemID)
		}
		if err == nil && out.ProgressApplied {
			s.nativeSocketProgress(r.Context(), uid, out.SessionID)
		}
		results = append(results, entry)
	}
	if batch {
		writeJSON(w, 200, nativeObject{"results": results})
	} else {
		writeJSON(w, 200, nativeObject{"success": true})
	}
}
