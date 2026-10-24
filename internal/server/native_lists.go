package server

import (
	"context"
	"net/http"
	"strings"

	"github.com/abjelosevic88/pocket-homelab-audiobooks/internal/library"
	"github.com/go-chi/chi/v5"
)

func (s *Server) nativeListWire(ctx context.Context, uid int64, l library.MediaList) (nativeObject, error) {
	a, err := s.catalog().Account(ctx, uid)
	if err != nil {
		return nil, err
	}
	out := nativeObject{"can_edit": l.Kind == "playlists" || a.Permissions.Update, "id": l.ID, "name": l.Name, "description": l.Description, "libraryId": l.LibraryID, "userId": l.AccountID, "createdAt": l.CreatedAt, "addedAt": l.CreatedAt, "updatedAt": l.UpdatedAt, "lastUpdate": l.UpdatedAt, "revision": l.Revision, "entries": l.Entries}
	items := []any{}
	for _, e := range l.Entries {
		it, err := s.nativeItem(ctx, uid, e.ItemID)
		if err != nil {
			return nil, err
		}
		if l.Kind == "collections" {
			items = append(items, it)
		} else {
			entry := nativeObject{"libraryItemId": e.ItemID, "episodeId": e.EpisodeID, "libraryItem": it}
			if e.EpisodeID != "" {
				for _, v := range nativeArray(nativeMap(it["media"])["episodes"]) {
					if nativeString(nativeMap(v)["id"]) == e.EpisodeID {
						entry["episode"] = v
						break
					}
				}
			}
			items = append(items, entry)
		}
	}
	if l.Kind == "collections" {
		out["books"] = items
		out["numBooks"] = len(items)
	} else {
		out["items"] = items
	}
	return out, nil
}
func (s *Server) nativeListAPI(w http.ResponseWriter, r *http.Request, uid int64, kind string, tail []string) {
	ctx := r.Context()
	c := s.catalog()
	read := r.Method == "GET" || r.Method == "HEAD"
	respond := func(l library.MediaList) {
		v, err := s.nativeListWire(ctx, uid, l)
		if err != nil {
			s.nativeError(w, err)
			return
		}
		writeJSON(w, 200, v)
	}
	decodeBody := func() (nativeObject, bool) {
		v := nativeObject{}
		if err := decode(r, &v); err != nil {
			writeErr(w, 400, "invalid JSON")
			return nil, false
		}
		return v, true
	}
	entries := func(v nativeObject) []library.ListEntry {
		out := []library.ListEntry{}
		if kind == "collections" {
			for _, id := range nativeArray(v["books"]) {
				out = append(out, library.ListEntry{ItemID: nativeString(id)})
			}
		} else {
			for _, entry := range nativeArray(v["items"]) {
				e := nativeMap(entry)
				out = append(out, library.ListEntry{ItemID: nativeString(e["libraryItemId"]), EpisodeID: nativeString(e["episodeId"])})
			}
		}
		return out
	}
	if len(tail) == 0 {
		if read {
			lists, err := c.Lists(ctx, uid, kind, r.URL.Query().Get("library"))
			if err != nil {
				s.nativeError(w, err)
				return
			}
			result := []any{}
			for _, l := range lists {
				v, err := s.nativeListWire(ctx, uid, l)
				if err != nil {
					s.nativeError(w, err)
					return
				}
				result = append(result, v)
			}
			writeJSON(w, 200, nativeObject{kind: result})
			return
		}
		if r.Method == "POST" {
			in, ok := decodeBody()
			if !ok {
				return
			}
			l, err := c.CreateList(ctx, uid, kind, nativeString(in["libraryId"]), nativeString(in["name"]), nativeString(in["description"]), entries(in))
			if err != nil {
				s.nativeError(w, err)
				return
			}
			respond(l)
			return
		}
	}
	if len(tail) == 0 {
		writeErr(w, 405, "method not allowed")
		return
	}
	id := tail[0]
	current, err := c.GetList(ctx, uid, kind, id)
	if err != nil {
		s.nativeError(w, err)
		return
	}
	if len(tail) == 1 {
		if read {
			respond(current)
			return
		}
		if r.Method == "DELETE" {
			if err = c.DeleteList(ctx, uid, kind, id); err != nil {
				s.nativeError(w, err)
				return
			}
			writeJSON(w, 200, nativeObject{"success": true})
			return
		}
		if r.Method == "PATCH" || r.Method == "PUT" {
			in, ok := decodeBody()
			if !ok {
				return
			}
			edit := library.ListEdit{}
			if v, ok := in["name"].(string); ok {
				edit.Name = &v
			}
			if v, ok := in["description"].(string); ok {
				edit.Description = &v
			}
			if v, ok := in["revision"].(float64); ok {
				revision := int64(v)
				edit.Expected = &revision
			}
			field := "books"
			if kind == "playlists" {
				field = "items"
			}
			if _, ok := in[field]; ok {
				value := entries(in)
				edit.Entries = &value
			}
			l, err := c.EditList(ctx, uid, kind, id, edit)
			if err != nil {
				s.nativeError(w, err)
				return
			}
			respond(l)
			return
		}
	}
	list := append([]library.ListEntry{}, current.Entries...)
	mutate := func(add bool, values []library.ListEntry) {
		for _, entry := range values {
			index := -1
			for i, e := range list {
				if e == entry {
					index = i
					break
				}
			}
			if add && index < 0 {
				list = append(list, entry)
			} else if !add && index >= 0 {
				list = append(list[:index], list[index+1:]...)
			}
		}
	}
	op := ""
	if len(tail) > 1 {
		op = tail[1]
	}
	switch {
	case r.Method == "POST" && (op == "book" || op == "item"):
		in, ok := decodeBody()
		if !ok {
			return
		}
		item := nativeString(in["libraryItemId"])
		if item == "" {
			item = nativeString(in["id"])
		}
		mutate(true, []library.ListEntry{{ItemID: item, EpisodeID: nativeString(in["episodeId"])}})
	case r.Method == "DELETE" && (op == "book" || op == "item") && len(tail) >= 3:
		episode := ""
		if len(tail) > 3 {
			episode = tail[3]
		}
		mutate(false, []library.ListEntry{{ItemID: tail[2], EpisodeID: episode}})
	case r.Method == "POST" && op == "batch" && len(tail) == 3 && (tail[2] == "add" || tail[2] == "remove"):
		in, ok := decodeBody()
		if !ok {
			return
		}
		mutate(tail[2] == "add", entries(in))
	default:
		writeErr(w, 405, "list operation not supported")
		return
	}
	l, err := c.EditList(ctx, uid, kind, id, library.ListEdit{Entries: &list, Expected: &current.Revision})
	if err != nil {
		s.nativeError(w, err)
		return
	}
	respond(l)
}
func (s *Server) nativeOrganize(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Backend != "native" {
		writeErr(w, 404, "native organisation is not active")
		return
	}
	path := chi.URLParam(r, "*")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) == 0 || (parts[0] != "collections" && parts[0] != "playlists") {
		writeErr(w, 404, "not found")
		return
	}
	s.nativeListAPI(w, r, actorID(r.Context()), parts[0], parts[1:])
}
