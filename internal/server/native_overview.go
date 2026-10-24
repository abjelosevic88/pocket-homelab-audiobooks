package server

import (
	"net/http"
	"time"
)

func (s *Server) nativeInProgress(w http.ResponseWriter, r *http.Request, uid int64) {
	progress, err := s.nativeProgress(r.Context(), uid)
	if err != nil {
		s.nativeError(w, err)
		return
	}
	out := []any{}
	limit := queryInt(r, "limit")
	if limit <= 0 {
		limit = 25
	}
	limit = min(limit, 500)
	for _, v := range progress {
		p := nativeMap(v)
		if nativeBool(p["isFinished"]) || nativeBool(p["hideFromContinueListening"]) || (nativeNumber(p["currentTime"]) <= 0 && nativeNumber(p["ebookProgress"]) <= 0) {
			continue
		}
		it, err := s.nativeItem(r.Context(), uid, nativeString(p["libraryItemId"]))
		if err != nil {
			s.nativeError(w, err)
			return
		}
		it["userMediaProgress"] = p
		it["progressLastUpdate"] = p["lastUpdate"]
		if episode := nativeString(p["episodeId"]); episode != "" {
			for _, e := range nativeArray(nativeMap(it["media"])["episodes"]) {
				if nativeString(nativeMap(e)["id"]) == episode {
					it["recentEpisode"] = e
					break
				}
			}
		}
		out = append(out, it)
		if len(out) == limit {
			break
		}
	}
	writeJSON(w, 200, nativeObject{"libraryItems": out})
}

func (s *Server) nativeListeningStats(w http.ResponseWriter, r *http.Request, uid int64) {
	all, err := s.nativeSessions(r.Context(), uid)
	if err != nil {
		s.nativeError(w, err)
		return
	}
	a, err := s.catalog().Account(r.Context(), uid)
	if err != nil {
		s.nativeError(w, err)
		return
	}
	// One grouped read of the daily splits replaces a query per native session.
	rows, err := s.store.DB.QueryContext(r.Context(), `SELECT d.session_id,d.day,d.seconds FROM catalog_play_days d JOIN catalog_play_sessions p ON p.id=d.session_id WHERE p.account_id=?`, a.ID)
	if err != nil {
		s.nativeError(w, err)
		return
	}
	split := map[string]map[string]float64{}
	for rows.Next() {
		var id, day string
		var secs float64
		if err = rows.Scan(&id, &day, &secs); err != nil {
			rows.Close()
			s.nativeError(w, err)
			return
		}
		if split[id] == nil {
			split[id] = map[string]float64{}
		}
		split[id][day] += secs
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		s.nativeError(w, err)
		return
	}
	days := map[string]float64{}
	items := map[string]bool{}
	total := 0.0
	for _, sess := range all {
		sec := nativeNumber(sess["timeListening"])
		if sec <= 0 {
			continue
		}
		total += sec
		items[nativeString(sess["libraryItemId"])] = true
		// Native sessions can cross midnight. Daily splits preserve wall time.
		if nativeString(sess["mediaPlayer"]) == "pocket-audiobooks-native" {
			for day, secs := range split[nativeString(sess["id"])] {
				days[day] += secs
			}
		} else {
			day := nativeString(sess["date"])
			if len(day) != 10 {
				day = time.UnixMilli(nativeMillis(sess["startedAt"])).In(time.Local).Format("2006-01-02")
			}
			days[day] += sec
		}
	}
	writeJSON(w, 200, nativeObject{"totalTime": total, "today": days[time.Now().Format("2006-01-02")], "days": days, "numItems": len(items), "recentSessions": all[:min(10, len(all))]})
}
