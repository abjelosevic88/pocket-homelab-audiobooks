package server

import (
	"github.com/abjelosevic88/pocket-homelab-audiobooks/internal/library"
	"net/http"
)

func (s *Server) nativeAccounts(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Backend != "native" || userFrom(r.Context()).Role != "admin" {
		writeErr(w, 403, "account management requires an administrator")
		return
	}
	ctx := r.Context()
	uid := actorID(ctx)
	if r.Method == "GET" {
		accounts, err := s.catalog().ManagedAccounts(ctx, uid)
		if err != nil {
			s.nativeError(w, err)
			return
		}
		// Administration lists library names without exposing books or listening.
		rows, err := s.store.DB.QueryContext(ctx, `SELECT id,name FROM catalog_libraries ORDER BY display_order,name`)
		if err != nil {
			s.nativeError(w, err)
			return
		}
		libs := []nativeObject{}
		for rows.Next() {
			var id, name string
			if err = rows.Scan(&id, &name); err != nil {
				rows.Close()
				s.nativeError(w, err)
				return
			}
			libs = append(libs, nativeObject{"id": id, "name": name})
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			s.nativeError(w, err)
			return
		}
		users, err := s.store.ListUsers(ctx)
		if err != nil {
			s.nativeError(w, err)
			return
		}
		people := []nativeObject{}
		for _, u := range users {
			people = append(people, nativeObject{"id": u.ID, "name": u.Name, "email": u.Email})
		}
		writeJSON(w, 200, nativeObject{"accounts": accounts, "libraries": libs, "users": people})
		return
	}
	var in library.ManagedAccount
	if err := decode(r, &in); err != nil {
		writeErr(w, 400, "invalid account")
		return
	}
	out, err := s.catalog().SaveAccount(ctx, uid, in)
	if err != nil {
		s.nativeError(w, err)
		return
	}
	// Re-authentication is required after any binding or permission change.
	s.nativeSockets.mu.Lock()
	for _, client := range s.nativeSockets.clients {
		client.mu.Lock()
		matches := in.UserID != nil && client.uid == *in.UserID
		client.mu.Unlock()
		if matches {
			client.stop()
		}
	}
	s.nativeSockets.mu.Unlock()
	writeJSON(w, 200, out)
}
