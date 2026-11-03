package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/abjelosevic88/pocket-homelab-audiobooks/internal/library"
	"github.com/abjelosevic88/pocket-homelab-audiobooks/internal/store"
	core "github.com/abjelosevic88/pocket-homelab-core/sdk"
)

// "Get it": an audiobook's page can ask Pocket Requests (through Pocket Core,
// link requests:read + requests:write) for the same book as an ebook, and an
// author's page lists more of their books with what is here, on its way or
// can be asked for. Audiobooks talks to Requests on the person's behalf; the
// browser never sees the link token. Without the link everything stays
// visible, greyed out with the reason.

var requestsUse = core.Use{App: "requests", Scopes: []string{"requests:read", "requests:write"},
	Reason: "Ask for the ebook of an audiobook and more by its author (Get buttons), and show whether it is on its way"}

// rqClient reaches the person's Pocket Requests with a fresh link token.
type rqClient struct {
	base, public, token string
}

// rqError is Requests answering with an error.
type rqError struct {
	status    int
	msg       string
	requestID int64
	openURL   string
}

func (e *rqError) Error() string { return e.msg }

var rqHTTP = &http.Client{Timeout: 45 * time.Second}

func (c *rqClient) do(ctx context.Context, method, path string, q url.Values, body, out any) error {
	u := c.base + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := rqHTTP.Do(req)
	if err != nil {
		return fmt.Errorf("Pocket Requests didn't answer: %w", err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if res.StatusCode < 200 || res.StatusCode > 299 {
		var e struct {
			Error     string `json:"error"`
			RequestID int64  `json:"request_id"`
			OpenURL   string `json:"open_url"`
		}
		_ = json.Unmarshal(b, &e)
		msg := strings.TrimSpace(e.Error)
		if msg == "" {
			msg = fmt.Sprintf("Pocket Requests answered HTTP %d", res.StatusCode)
		}
		return &rqError{status: res.StatusCode, msg: msg, requestID: e.RequestID, openURL: e.OpenURL}
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(b, out)
}

func (c *rqClient) requestURL(id int64) string {
	if c.public == "" || id == 0 {
		return ""
	}
	return c.public + "/requests/" + strconv.FormatInt(id, 10)
}

// rqActAs asks Core for a link token (a field so tests can stand in for Core).
func (s *Server) rqActAs(ctx context.Context, email string) (*core.LinkToken, error) {
	if s.rqLink != nil {
		return s.rqLink(ctx, email)
	}
	return s.core.ActAs(ctx, email, "requests", "requests:read", "requests:write")
}

// rqFor reaches Requests for a person, or says in plain words why it can't.
func (s *Server) rqFor(ctx context.Context, u *store.User) (*rqClient, string) {
	if s.rqLink == nil && !s.core.Configured() {
		return nil, "Needs Pocket Core: books are asked for in Pocket Requests, through Pocket Core."
	}
	lt, err := s.rqActAs(ctx, u.Email)
	if err != nil {
		var le *core.LinkError
		if errors.As(err, &le) {
			return nil, rqLinkReason(le)
		}
		return nil, "Pocket Core: " + err.Error()
	}
	return &rqClient{base: strings.TrimRight(lt.URL, "/"), public: strings.TrimRight(lt.PublicURL, "/"), token: lt.AccessToken}, ""
}

func rqLinkReason(le *core.LinkError) string {
	switch le.Reason {
	case "target_missing":
		return "Pocket Requests is not on this box."
	case "off_person":
		return "Switched off for you: turn on “Pocket Audiobooks → Pocket Requests” in Pocket Core, Settings → Apps acting for me."
	case "off_admin":
		return "Switched off for this box: an admin turns on “Pocket Audiobooks → Pocket Requests” in Pocket Core, Links."
	case "not_in_audience":
		return "You have no Pocket Requests: an admin can give you access in Pocket Core."
	case "not_offered", "not_declared":
		return "This Pocket Requests or Audiobooks is too old to link the two; update both."
	case "unknown_user":
		return "Your account is not in Pocket Core."
	}
	return "Pocket Requests: " + le.Message
}

// ---- what Requests says about a book ----

type rqHave struct {
	ID  string `json:"id,omitempty"`
	URL string `json:"url"`
}

type rqAsk struct {
	ID       int64   `json:"id"`
	Kind     string  `json:"kind"`
	Language string  `json:"language,omitempty"`
	Status   string  `json:"status"`
	State    string  `json:"state"`
	Progress float64 `json:"progress"`
	URL      string  `json:"url,omitempty"` // the request's page (filled in here)
}

type rqTitle struct {
	BookKey  string `json:"book_key"`
	Title    string `json:"title"`
	Subtitle string `json:"subtitle"`
	Year     int    `json:"year"`
	Poster   string `json:"poster"`
	Book     struct {
		Have struct {
			Ebook     *rqHave `json:"ebook"`
			Audiobook *rqHave `json:"audiobook"`
		} `json:"have"`
		Asked []rqAsk `json:"asked"`
	} `json:"book"`
}

type rqCanGet struct {
	OK     bool   `json:"ok"`
	Reason string `json:"reason,omitempty"`
}

// getItBook is one book as the pages show it.
type getItBook struct {
	BookKey  string  `json:"book_key"`
	Title    string  `json:"title"`
	Subtitle string  `json:"subtitle"`
	Year     int     `json:"year"`
	Cover    string  `json:"cover"`
	Ebook    *rqHave `json:"ebook"`     // here as an ebook (open it in Pocket Library)
	Audio    *rqHave `json:"audiobook"` // here as an audiobook
	Asked    []rqAsk `json:"asked"`     // the person's live requests for it
}

func (c *rqClient) book(t rqTitle) getItBook {
	b := getItBook{BookKey: t.BookKey, Title: t.Title, Subtitle: t.Subtitle, Year: t.Year, Cover: t.Poster, Ebook: t.Book.Have.Ebook,
		Audio: t.Book.Have.Audiobook, Asked: []rqAsk{}}
	for _, a := range t.Book.Asked {
		if a.Status == "declined" || a.Status == "cancelled" || a.State == "removed" {
			continue
		}
		a.URL = c.requestURL(a.ID)
		b.Asked = append(b.Asked, a)
	}
	return b
}

// getIt is what a page shows: can books be asked for, and the book(s).
type getIt struct {
	Available bool        `json:"available"`        // Requests can be reached and can fetch books
	Reason    string      `json:"reason,omitempty"` // why not (greyed out with this)
	Found     bool        `json:"found,omitempty"`  // Requests knows the book
	Book      *getItBook  `json:"book,omitempty"`
	Items     []getItBook `json:"items,omitempty"` // an author's books
	Languages []string    `json:"languages"`       // languages a book may be asked for in
	AppURL    string      `json:"app_url,omitempty"`
}

type getItCached struct {
	v  getIt
	at time.Time
}

var (
	getItMu    sync.Mutex
	getItCache = map[string]getItCached{}
)

const getItTTL = 45 * time.Second

func getItGet(key string) (getIt, bool) {
	getItMu.Lock()
	defer getItMu.Unlock()
	c, ok := getItCache[key]
	return c.v, ok && time.Since(c.at) < getItTTL
}

func getItPut(key string, v getIt) {
	getItMu.Lock()
	getItCache[key] = getItCached{v, time.Now()}
	getItMu.Unlock()
}

// forgetGetIt drops a person's cached answers (after they asked for something).
func forgetGetIt(uid int64) {
	pre := strconv.FormatInt(uid, 10) + ":"
	getItMu.Lock()
	for k := range getItCache {
		if strings.HasPrefix(k, pre) {
			delete(getItCache, k)
		}
	}
	getItMu.Unlock()
}

// itemFacts are what an audiobook tells Requests to find its book.
type itemFacts struct {
	Title, Author, ISBN, Language string
}

func facts(it library.Item) itemFacts {
	var doc struct {
		Authors []struct {
			Name string `json:"name"`
		} `json:"authors"`
		Media struct {
			Title    string `json:"title"`
			ISBN     string `json:"isbn"`
			Language string `json:"language"`
		} `json:"media"`
	}
	_ = json.Unmarshal(it.Document, &doc)
	f := itemFacts{Title: firstNonEmpty(doc.Media.Title, it.Title), ISBN: strings.TrimSpace(doc.Media.ISBN), Language: doc.Media.Language}
	if len(doc.Authors) > 0 {
		f.Author = doc.Authors[0].Name
	}
	if f.Author == "" {
		f.Author = strings.TrimSpace(strings.Split(it.Author, ",")[0])
	}
	// "Project Hail Mary (Unabridged)": the book's own title
	if i := strings.Index(f.Title, " ("); i > 3 && strings.Contains(strings.ToLower(f.Title[i:]), "abridged") {
		f.Title = f.Title[:i]
	}
	return f
}

func (s *Server) itemFor(ctx context.Context, uid int64, id string) (library.Item, error) {
	if s.cfg.Backend != "native" {
		return library.Item{}, library.ErrNotFound
	}
	return s.catalog().Item(ctx, uid, id)
}

// GET /api/v1/get-it/items/{id} — the audiobook page's "Get it" card.
func (s *Server) handleGetItItem(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := userFrom(ctx)
	it, err := s.itemFor(ctx, u.ID, chi.URLParam(r, "id"))
	if err != nil {
		s.catalogError(w, err)
		return
	}
	key := strconv.FormatInt(u.ID, 10) + ":item:" + it.ID
	if v, ok := getItGet(key); ok && r.URL.Query().Get("fresh") != "1" {
		writeJSON(w, http.StatusOK, v)
		return
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	v := s.fetchGetItItem(cctx, u, facts(it))
	getItPut(key, v)
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) fetchGetItItem(ctx context.Context, u *store.User, f itemFacts) getIt {
	c, why := s.rqFor(ctx, u)
	if c == nil {
		return getIt{Reason: why, Languages: []string{}}
	}
	out := getIt{Available: true, AppURL: c.public, Languages: []string{}}
	q := url.Values{"title": {f.Title}}
	if f.Author != "" {
		q.Set("author", f.Author)
	}
	if f.ISBN != "" {
		q.Set("isbn", f.ISBN)
	}
	var lk struct {
		Title     *rqTitle  `json:"title"`
		Languages []string  `json:"languages_wanted"`
		CanGet    *rqCanGet `json:"can_get"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/v1/books/lookup", q, nil, &lk); err != nil {
		var re *rqError
		if errors.As(err, &re) && re.status == http.StatusNotFound {
			return getIt{Reason: "This Pocket Requests can't find books yet; update it.", Languages: []string{}}
		}
		return getIt{Reason: err.Error(), Languages: []string{}}
	}
	if lk.Languages != nil {
		out.Languages = lk.Languages
	}
	if lk.CanGet != nil && !lk.CanGet.OK {
		out.Available, out.Reason = false, firstNonEmpty(lk.CanGet.Reason, "Pocket Requests can't fetch books right now.")
	}
	if lk.Title != nil {
		b := c.book(*lk.Title)
		out.Found, out.Book = true, &b
	}
	return out
}

// GET /api/v1/get-it/authors/{id} — "More by <author>" on the author page.
func (s *Server) handleGetItAuthor(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := userFrom(ctx)
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	if s.cfg.Backend == "native" {
		if a, err := s.catalog().Author(ctx, u.ID, chi.URLParam(r, "id")); err == nil {
			name = a.Name
		}
	}
	if name == "" {
		writeErr(w, http.StatusNotFound, "no such author")
		return
	}
	key := strconv.FormatInt(u.ID, 10) + ":author:" + strings.ToLower(name)
	if v, ok := getItGet(key); ok && r.URL.Query().Get("fresh") != "1" {
		writeJSON(w, http.StatusOK, v)
		return
	}
	cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	c, why := s.rqFor(cctx, u)
	if c == nil {
		writeJSON(w, http.StatusOK, getIt{Reason: why, Items: []getItBook{}, Languages: []string{}})
		return
	}
	var res struct {
		Items     []rqTitle `json:"items"`
		Languages []string  `json:"languages_wanted"`
		CanGet    *rqCanGet `json:"can_get"`
	}
	if err := c.do(cctx, http.MethodGet, "/api/v1/books/author", url.Values{"name": {name}}, nil, &res); err != nil {
		var re *rqError
		reason := err.Error()
		if errors.As(err, &re) && re.status == http.StatusNotFound {
			reason = "This Pocket Requests can't list an author's books yet; update it."
		}
		writeJSON(w, http.StatusOK, getIt{Reason: reason, Items: []getItBook{}, Languages: []string{}})
		return
	}
	v := getIt{Available: true, AppURL: c.public, Items: []getItBook{}, Languages: []string{}}
	if res.Languages != nil {
		v.Languages = res.Languages
	}
	if res.CanGet != nil && !res.CanGet.OK {
		v.Available, v.Reason = false, firstNonEmpty(res.CanGet.Reason, "Pocket Requests can't fetch books right now.")
	}
	for _, t := range res.Items {
		v.Items = append(v.Items, c.book(t))
	}
	getItPut(key, v)
	writeJSON(w, http.StatusOK, v)
}

// POST /api/v1/get-it {"kind","book_key","item_id","title","author","isbn","language","note"} — ask for it.
func (s *Server) handleGetItAsk(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := userFrom(ctx)
	var in struct {
		Kind     string `json:"kind"`
		BookKey  string `json:"book_key"`
		ItemID   string `json:"item_id"`
		Title    string `json:"title"`
		Author   string `json:"author"`
		ISBN     string `json:"isbn"`
		Language string `json:"language"`
		Note     string `json:"note"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if in.Kind != "ebook" && in.Kind != "audiobook" {
		writeErr(w, http.StatusBadRequest, "kind is ebook or audiobook")
		return
	}
	body := map[string]any{"kind": in.Kind}
	if in.ItemID != "" {
		// an audiobook here: its own title, author and ISBN
		it, err := s.itemFor(ctx, u.ID, in.ItemID)
		if err != nil {
			s.catalogError(w, err)
			return
		}
		f := facts(it)
		in.Title, in.Author, in.ISBN = f.Title, f.Author, firstNonEmpty(in.ISBN, f.ISBN)
	}
	for k, v := range map[string]string{"book_key": in.BookKey, "title": in.Title, "author": in.Author, "isbn": in.ISBN} {
		if v = strings.TrimSpace(v); v != "" {
			body[k] = v
		}
	}
	if body["book_key"] == nil && body["title"] == nil && body["isbn"] == nil {
		writeErr(w, http.StatusBadRequest, "say which book")
		return
	}
	if l := strings.ToLower(strings.TrimSpace(in.Language)); l != "" && l != "any" && len(l) <= 3 {
		body["language"] = l
	}
	if note := strings.TrimSpace(in.Note); note != "" {
		if len(note) > 1000 {
			note = note[:1000]
		}
		body["note"] = note
	}
	cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	c, why := s.rqFor(cctx, u)
	if c == nil {
		writeErr(w, http.StatusConflict, why)
		return
	}
	var made rqAsk
	err := c.do(cctx, http.MethodPost, "/api/v1/requests", nil, body, &made)
	forgetGetIt(u.ID)
	if err != nil {
		var re *rqError
		if errors.As(err, &re) {
			status := re.status
			if status < 400 || status > 599 {
				status = http.StatusBadGateway
			}
			out := map[string]any{"error": re.msg}
			if re.requestID > 0 {
				out["request_id"], out["url"] = re.requestID, c.requestURL(re.requestID)
			} else if re.openURL != "" {
				out["url"] = re.openURL
			}
			writeJSON(w, status, out)
			return
		}
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	made.URL = c.requestURL(made.ID)
	writeJSON(w, http.StatusCreated, made)
}
