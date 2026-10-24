// Package abs is a small client for the Audiobookshelf API (2.26+): the
// person's libraries, books, series and authors, their progress and
// bookmarks, playback sessions and listening history. Every call acts as
// one Audiobookshelf account (an API key, or the access token of a
// password sign-in that is refreshed on demand).
package abs

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Client talks to one Audiobookshelf server as one account.
type Client struct {
	Base      string // http(s)://host[:port][/audiobookshelf]
	UserAgent string
	HTTP      *http.Client

	mu    sync.Mutex
	token string
	// Refresh, when set, is called once after a 401 to get a fresh access
	// token (password sign-ins: access tokens live an hour).
	Refresh func(ctx context.Context) (string, error)
}

// New returns a client for base acting with token.
func New(base, token string) *Client {
	return &Client{Base: strings.TrimRight(base, "/"), token: token, UserAgent: "PocketAudiobooks/1.0",
		HTTP: &http.Client{Timeout: 30 * time.Second}}
}

// Token is the access token in use.
func (c *Client) Token() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.token
}

// SetToken replaces the access token.
func (c *Client) SetToken(t string) {
	c.mu.Lock()
	c.token = t
	c.mu.Unlock()
}

// Error is a non-2xx answer.
type Error struct {
	Status int
	Msg    string
}

func (e *Error) Error() string {
	if e.Msg == "" {
		return fmt.Sprintf("Audiobookshelf answered HTTP %d", e.Status)
	}
	return fmt.Sprintf("Audiobookshelf answered HTTP %d: %s", e.Status, e.Msg)
}

// IsNotFound reports a 404.
func IsNotFound(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.Status == http.StatusNotFound
}

// IsAuth reports a rejected token.
func IsAuth(err error) bool {
	var e *Error
	return errors.As(err, &e) && (e.Status == http.StatusUnauthorized || e.Status == http.StatusForbidden)
}

// Request builds an authenticated request (for streaming through the app).
func (c *Client) Request(ctx context.Context, method, path string, q url.Values) (*http.Request, error) {
	u := c.Base + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token())
	req.Header.Set("User-Agent", c.UserAgent)
	return req, nil
}

// Do sends a prepared request, refreshing the token once on a 401.
func (c *Client) Do(req *http.Request) (*http.Response, error) { return c.DoWith(c.HTTP, req) }

// DoWith is Do with another HTTP client (streams: no overall timeout).
func (c *Client) DoWith(hc *http.Client, req *http.Request) (*http.Response, error) {
	res, err := hc.Do(req)
	if err != nil || res.StatusCode != http.StatusUnauthorized || c.Refresh == nil || req.Body != nil && req.GetBody == nil {
		return res, err
	}
	res.Body.Close()
	tok, rerr := c.Refresh(req.Context())
	if rerr != nil {
		return nil, &Error{Status: http.StatusUnauthorized, Msg: "the sign-in expired: " + rerr.Error()}
	}
	c.SetToken(tok)
	r2 := req.Clone(req.Context())
	if req.GetBody != nil {
		r2.Body, _ = req.GetBody()
	}
	r2.Header.Set("Authorization", "Bearer "+tok)
	return hc.Do(r2)
}

func (c *Client) call(ctx context.Context, method, path string, q url.Values, body, out any) error {
	var rd io.Reader
	var raw []byte
	if body != nil {
		raw, _ = json.Marshal(body)
		rd = bytes.NewReader(raw)
	}
	u := c.Base + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
		req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(raw)), nil }
	}
	req.Header.Set("Authorization", "Bearer "+c.Token())
	req.Header.Set("User-Agent", c.UserAgent)
	req.Header.Set("Accept", "application/json")
	res, err := c.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 2048))
		msg := strings.TrimSpace(string(b))
		var j struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(b, &j) == nil && j.Error != "" {
			msg = j.Error
		}
		if len(msg) > 300 || strings.HasPrefix(msg, "<") {
			msg = ""
		}
		return &Error{Status: res.StatusCode, Msg: msg}
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, res.Body)
		return nil
	}
	return json.NewDecoder(res.Body).Decode(out)
}

// ---- server ----

// Status is the unauthenticated /status answer.
type Status struct {
	App           string   `json:"app"`
	ServerVersion string   `json:"serverVersion"`
	IsInit        bool     `json:"isInit"`
	AuthMethods   []string `json:"authMethods"`
}

// Status reads /status (no token needed).
func (c *Client) Status(ctx context.Context) (*Status, error) {
	var st Status
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.Base+"/status", nil)
	if err != nil {
		return nil, err
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, &Error{Status: res.StatusCode}
	}
	if err := json.NewDecoder(res.Body).Decode(&st); err != nil || st.App != "audiobookshelf" {
		return nil, errors.New("this address does not answer like Audiobookshelf")
	}
	return &st, nil
}

// LocalLogin reports whether the server accepts username + password.
func (st *Status) LocalLogin() bool {
	for _, m := range st.AuthMethods {
		if m == "local" {
			return true
		}
	}
	return false
}

// LoginResult is what a password sign-in returns.
type LoginResult struct {
	User         Me
	AccessToken  string
	RefreshToken string
}

// Login signs in with a password and asks for tokens in the answer (as the
// phone apps do), so the refresh token can be kept instead of the password.
func (c *Client) Login(ctx context.Context, username, password string) (*LoginResult, error) {
	b, _ := json.Marshal(map[string]string{"username": username, "password": password})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Base+"/login", bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-return-tokens", "true")
	req.Header.Set("User-Agent", c.UserAgent)
	return c.tokens(req)
}

// RefreshWith trades a refresh token for a new access token (and a new
// refresh token: Audiobookshelf rotates them).
func (c *Client) RefreshWith(ctx context.Context, refresh string) (*LoginResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Base+"/auth/refresh", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("x-refresh-token", refresh)
	req.Header.Set("User-Agent", c.UserAgent)
	return c.tokens(req)
}

func (c *Client) tokens(req *http.Request) (*LoginResult, error) {
	res, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusUnauthorized {
		return nil, &Error{Status: 401, Msg: "wrong username or password"}
	}
	if res.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 512))
		msg := strings.TrimSpace(string(b))
		if strings.HasPrefix(msg, "<") {
			msg = ""
		}
		return nil, &Error{Status: res.StatusCode, Msg: msg}
	}
	var out struct {
		User struct {
			Me
			AccessToken  string `json:"accessToken"`
			RefreshToken string `json:"refreshToken"`
			Token        string `json:"token"`
		} `json:"user"`
	}
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		return nil, err
	}
	r := &LoginResult{User: out.User.Me, AccessToken: out.User.AccessToken, RefreshToken: out.User.RefreshToken}
	if r.AccessToken == "" {
		r.AccessToken = out.User.Token // before 2.26
	}
	if r.AccessToken == "" {
		return nil, errors.New("Audiobookshelf did not hand out a token")
	}
	return r, nil
}

// Logout ends a password sign-in's refresh token.
func (c *Client) Logout(ctx context.Context, refresh string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Base+"/logout", nil)
	if err != nil {
		return err
	}
	req.Header.Set("x-refresh-token", refresh)
	res, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	res.Body.Close()
	return nil
}

// ---- the account ----

// Me is the signed-in account with its progress and bookmarks.
type Me struct {
	Permissions struct {
		Upload bool `json:"upload"`
	} `json:"permissions"`
	ID                  string          `json:"id"`
	Username            string          `json:"username"`
	Email               string          `json:"email"`
	Type                string          `json:"type"` // root | admin | user | guest
	MediaProgress       []MediaProgress `json:"mediaProgress"`
	Bookmarks           []Bookmark      `json:"bookmarks"`
	LibrariesAccessible []string        `json:"librariesAccessible"`
	IsActive            bool            `json:"isActive"`
}

// MediaProgress is where an account is in one book.
type MediaProgress struct {
	ID                        string  `json:"id"`
	LibraryItemID             string  `json:"libraryItemId"`
	EpisodeID                 string  `json:"episodeId"`
	MediaItemType             string  `json:"mediaItemType"`
	Duration                  float64 `json:"duration"`
	Progress                  float64 `json:"progress"`
	CurrentTime               float64 `json:"currentTime"`
	IsFinished                bool    `json:"isFinished"`
	HideFromContinueListening bool    `json:"hideFromContinueListening"`
	LastUpdate                int64   `json:"lastUpdate"`
	StartedAt                 int64   `json:"startedAt"`
	FinishedAt                int64   `json:"finishedAt"`
}

// Bookmark is a saved moment in a book.
type Bookmark struct {
	LibraryItemID string  `json:"libraryItemId"`
	Title         string  `json:"title"`
	Time          float64 `json:"time"`
	CreatedAt     int64   `json:"createdAt"`
}

// Me reads the account.
func (c *Client) Me(ctx context.Context) (*Me, error) {
	var m Me
	return &m, c.call(ctx, http.MethodGet, "/api/me", nil, nil, &m)
}

// UpdateProgress sets the account's progress in a book (time, or finished).
func (c *Client) UpdateProgress(ctx context.Context, itemID string, body map[string]any) error {
	return c.call(ctx, http.MethodPatch, "/api/me/progress/"+url.PathEscape(itemID), nil, body, nil)
}

// RemoveProgress forgets the progress row (by its id, not the item's).
func (c *Client) RemoveProgress(ctx context.Context, progressID string) error {
	return c.call(ctx, http.MethodDelete, "/api/me/progress/"+url.PathEscape(progressID), nil, nil, nil)
}

// HideFromContinue takes a book off "continue listening" without touching its progress.
func (c *Client) HideFromContinue(ctx context.Context, progressID string) error {
	return c.call(ctx, http.MethodGet, "/api/me/progress/"+url.PathEscape(progressID)+"/remove-from-continue-listening", nil, nil, nil)
}

// AddBookmark saves a moment.
func (c *Client) AddBookmark(ctx context.Context, itemID string, t float64, title string) (*Bookmark, error) {
	var b Bookmark
	return &b, c.call(ctx, http.MethodPost, "/api/me/item/"+url.PathEscape(itemID)+"/bookmark", nil, map[string]any{"time": t, "title": title}, &b)
}

// RenameBookmark changes a bookmark's title.
func (c *Client) RenameBookmark(ctx context.Context, itemID string, t float64, title string) error {
	return c.call(ctx, http.MethodPatch, "/api/me/item/"+url.PathEscape(itemID)+"/bookmark", nil, map[string]any{"time": t, "title": title}, nil)
}

// RemoveBookmark deletes the bookmark at a time.
func (c *Client) RemoveBookmark(ctx context.Context, itemID string, t float64) error {
	return c.call(ctx, http.MethodDelete, fmt.Sprintf("/api/me/item/%s/bookmark/%s", url.PathEscape(itemID), trimFloat(t)), nil, nil, nil)
}

func trimFloat(f float64) string {
	s := fmt.Sprintf("%.6f", f)
	s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	return s
}

// ---- libraries ----

// Library is one library on the server.
type Library struct {
	Folders []struct {
		ID       string `json:"id"`
		FullPath string `json:"fullPath"`
	} `json:"folders"`
	ID           string `json:"id"`
	Name         string `json:"name"`
	MediaType    string `json:"mediaType"` // book | podcast
	Icon         string `json:"icon"`
	DisplayOrder int    `json:"displayOrder"`
}

// Libraries lists the libraries the account may open.
func (c *Client) Libraries(ctx context.Context) ([]Library, error) {
	var out struct {
		Libraries []Library `json:"libraries"`
	}
	return out.Libraries, c.call(ctx, http.MethodGet, "/api/libraries", nil, nil, &out)
}

// Author is a person who wrote books (expanded items, author pages).
type Author struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	ImagePath   string `json:"imagePath"`
	NumBooks    int    `json:"numBooks"`
	UpdatedAt   int64  `json:"updatedAt"`
}

// SeriesRef is a book's place in a series.
type SeriesRef struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Sequence string `json:"sequence"`
}

// Metadata describes a book.
type Metadata struct {
	Title         string      `json:"title"`
	Subtitle      string      `json:"subtitle"`
	AuthorName    string      `json:"authorName"`
	NarratorName  string      `json:"narratorName"`
	SeriesName    string      `json:"seriesName"`
	Genres        []string    `json:"genres"`
	PublishedYear string      `json:"publishedYear"`
	Publisher     string      `json:"publisher"`
	Description   string      `json:"description"`
	Language      string      `json:"language"`
	ISBN          string      `json:"isbn"`
	ASIN          string      `json:"asin"`
	Explicit      bool        `json:"explicit"`
	Abridged      bool        `json:"abridged"`
	Authors       []Author    `json:"authors"`   // expanded only
	Narrators     []string    `json:"narrators"` // expanded only
	Series        []SeriesRef `json:"-"`         // expanded only (see UnmarshalJSON)
}

// UnmarshalJSON accepts "series" as a list (expanded) or an object (in a series listing).
func (m *Metadata) UnmarshalJSON(b []byte) error {
	type plain Metadata
	var aux struct {
		plain
		Series json.RawMessage `json:"series"`
	}
	if err := json.Unmarshal(b, &aux); err != nil {
		return err
	}
	*m = Metadata(aux.plain)
	if len(aux.Series) > 0 && aux.Series[0] == '[' {
		_ = json.Unmarshal(aux.Series, &m.Series)
	} else if len(aux.Series) > 0 && aux.Series[0] == '{' {
		var one SeriesRef
		if json.Unmarshal(aux.Series, &one) == nil && one.ID != "" {
			m.Series = []SeriesRef{one}
		}
	}
	return nil
}

// Chapter is a named stretch of a book.
type Chapter struct {
	ID    int     `json:"id"`
	Start float64 `json:"start"`
	End   float64 `json:"end"`
	Title string  `json:"title"`
}

// AudioFile is one file of a book (expanded items).
type AudioFile struct {
	Index    int     `json:"index"`
	Ino      string  `json:"ino"`
	Duration float64 `json:"duration"`
	MimeType string  `json:"mimeType"`
	Codec    string  `json:"codec"`
	BitRate  int     `json:"bitRate"`
	Exclude  bool    `json:"exclude"`
	Metadata struct {
		Filename string `json:"filename"`
		Size     int64  `json:"size"`
	} `json:"metadata"`
}

// Media is a book's content.
type Media struct {
	ID            string      `json:"id"`
	Metadata      Metadata    `json:"metadata"`
	CoverPath     string      `json:"coverPath"`
	Tags          []string    `json:"tags"`
	NumTracks     int         `json:"numTracks"`
	NumAudioFiles int         `json:"numAudioFiles"`
	NumChapters   int         `json:"numChapters"`
	Duration      float64     `json:"duration"`
	Size          int64       `json:"size"`
	Chapters      []Chapter   `json:"chapters"`   // expanded only
	AudioFiles    []AudioFile `json:"audioFiles"` // expanded only
	EbookFile     *struct {
		Ino string `json:"ino"`
	} `json:"ebookFile"`
}

// Item is one book (a library item).
type Item struct {
	ID                string         `json:"id"`
	LibraryID         string         `json:"libraryId"`
	MediaType         string         `json:"mediaType"`
	AddedAt           int64          `json:"addedAt"`
	UpdatedAt         int64          `json:"updatedAt"`
	IsMissing         bool           `json:"isMissing"`
	IsInvalid         bool           `json:"isInvalid"`
	Media             Media          `json:"media"`
	UserMediaProgress *MediaProgress `json:"userMediaProgress"`
	// in a series listing / series filter: the book's number
	Sequence string `json:"sequence"`
	// collapsed series in a listing
	CollapsedSeries *struct {
		ID         string `json:"id"`
		Name       string `json:"name"`
		NumBooks   int    `json:"numBooks"`
		SeriesSeq  string `json:"seriesSequenceList"`
		LibraryIDs []string
	} `json:"collapsedSeries"`
}

// Page is one page of a listing.
type Page struct {
	Results []Item `json:"results"`
	Total   int    `json:"total"`
	Limit   int    `json:"limit"`
	Page    int    `json:"page"`
}

// Filter encodes a library filter ("progress", "finished") the way Audiobookshelf reads it.
func Filter(group, value string) string {
	return group + "." + base64.StdEncoding.EncodeToString([]byte(value))
}

// ItemsQuery picks a page of a library.
type ItemsQuery struct {
	Sort   string // addedAt | media.metadata.title | media.metadata.authorNameLF | media.duration | media.metadata.publishedYear | sequence | progress | random
	Desc   bool
	Filter string // from Filter()
	Limit  int
	Page   int
}

// Items lists a library's books.
func (c *Client) Items(ctx context.Context, libraryID string, q ItemsQuery) (*Page, error) {
	v := url.Values{"minified": {"1"}, "limit": {fmt.Sprint(q.Limit)}, "page": {fmt.Sprint(q.Page)}}
	if q.Sort != "" {
		v.Set("sort", q.Sort)
	}
	if q.Desc {
		v.Set("desc", "1")
	}
	if q.Filter != "" {
		v.Set("filter", q.Filter)
	}
	var p Page
	return &p, c.call(ctx, http.MethodGet, "/api/libraries/"+url.PathEscape(libraryID)+"/items", v, nil, &p)
}

// Item reads one book with its chapters, files, authors and series.
func (c *Client) Item(ctx context.Context, id string) (*Item, error) {
	var it Item
	return &it, c.call(ctx, http.MethodGet, "/api/items/"+url.PathEscape(id), url.Values{"expanded": {"1"}, "include": {"progress"}}, nil, &it)
}

// Shelf is one row of a library's personalised home.
type Shelf struct {
	ID       string            `json:"id"` // continue-listening | continue-series | recently-added | recent-series | discover | listen-again | newest-authors
	Label    string            `json:"label"`
	Type     string            `json:"type"` // book | series | authors
	Total    int               `json:"total"`
	Entities []json.RawMessage `json:"entities"`
}

// Personalized reads a library's home shelves for the account.
func (c *Client) Personalized(ctx context.Context, libraryID string, limit int) ([]Shelf, error) {
	var out []Shelf
	return out, c.call(ctx, http.MethodGet, "/api/libraries/"+url.PathEscape(libraryID)+"/personalized", url.Values{"limit": {fmt.Sprint(limit)}}, nil, &out)
}

// Series is a series in a library listing.
type Series struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	AddedAt int64  `json:"addedAt"`
	Books   []Item `json:"books"`
}

// SeriesPage is a page of series.
type SeriesPage struct {
	Results []Series `json:"results"`
	Total   int      `json:"total"`
}

// SeriesList lists a library's series (each with its books).
func (c *Client) SeriesList(ctx context.Context, libraryID, sort string, desc bool, limit, page int) (*SeriesPage, error) {
	v := url.Values{"limit": {fmt.Sprint(limit)}, "page": {fmt.Sprint(page)}, "minified": {"1"}}
	if sort != "" {
		v.Set("sort", sort)
	}
	if desc {
		v.Set("desc", "1")
	}
	var p SeriesPage
	return &p, c.call(ctx, http.MethodGet, "/api/libraries/"+url.PathEscape(libraryID)+"/series", v, nil, &p)
}

// SeriesInfo reads one series' name.
func (c *Client) SeriesInfo(ctx context.Context, libraryID, seriesID string) (*Series, error) {
	var s Series
	return &s, c.call(ctx, http.MethodGet, "/api/libraries/"+url.PathEscape(libraryID)+"/series/"+url.PathEscape(seriesID), nil, nil, &s)
}

// Authors lists a library's authors.
func (c *Client) Authors(ctx context.Context, libraryID string) ([]Author, error) {
	var out struct {
		Authors []Author `json:"authors"`
	}
	return out.Authors, c.call(ctx, http.MethodGet, "/api/libraries/"+url.PathEscape(libraryID)+"/authors", nil, nil, &out)
}

// AuthorWithItems is an author with their books.
type AuthorWithItems struct {
	Author
	LibraryItems []Item `json:"libraryItems"`
}

// Author reads one author with their books.
func (c *Client) Author(ctx context.Context, id string) (*AuthorWithItems, error) {
	var a AuthorWithItems
	return &a, c.call(ctx, http.MethodGet, "/api/authors/"+url.PathEscape(id), url.Values{"include": {"items"}}, nil, &a)
}

// SearchResult is what a library search finds.
type SearchResult struct {
	Book []struct {
		LibraryItem Item `json:"libraryItem"`
	} `json:"book"`
	Authors   []Author `json:"authors"`
	Narrators []struct {
		Name     string `json:"name"`
		NumBooks int    `json:"numBooks"`
	} `json:"narrators"`
	Series []struct {
		Series Series `json:"series"`
		Books  []Item `json:"books"`
	} `json:"series"`
}

// Search searches one library.
func (c *Client) Search(ctx context.Context, libraryID, q string, limit int) (*SearchResult, error) {
	var r SearchResult
	return &r, c.call(ctx, http.MethodGet, "/api/libraries/"+url.PathEscape(libraryID)+"/search", url.Values{"q": {q}, "limit": {fmt.Sprint(limit)}}, nil, &r)
}

// ---- playback ----

// DeviceInfo names the player in Audiobookshelf's sessions.
type DeviceInfo struct {
	ID            string `json:"id,omitempty"`
	DeviceID      string `json:"deviceId,omitempty"`
	ClientName    string `json:"clientName,omitempty"`
	ClientVersion string `json:"clientVersion,omitempty"`
	DeviceName    string `json:"deviceName,omitempty"`
	OSName        string `json:"osName,omitempty"`
	BrowserName   string `json:"browserName,omitempty"`
	Model         string `json:"model,omitempty"`
	Manufacturer  string `json:"manufacturer,omitempty"`
}

// Track is one playable file of a session (or the HLS playlist of a transcode).
type Track struct {
	Index       int     `json:"index"`
	StartOffset float64 `json:"startOffset"`
	Duration    float64 `json:"duration"`
	Title       string  `json:"title"`
	ContentURL  string  `json:"contentUrl"`
	MimeType    string  `json:"mimeType"`
	Codec       string  `json:"codec"`
}

// Session is a playback session, open or saved (listening history).
type Session struct {
	ID            string      `json:"id"`
	UserID        string      `json:"userId"`
	LibraryID     string      `json:"libraryId"`
	LibraryItemID string      `json:"libraryItemId"`
	EpisodeID     string      `json:"episodeId"`
	MediaType     string      `json:"mediaType"`
	DisplayTitle  string      `json:"displayTitle"`
	DisplayAuthor string      `json:"displayAuthor"`
	Duration      float64     `json:"duration"`
	PlayMethod    int         `json:"playMethod"` // 0 direct play, 1 direct stream, 2 transcode, 3 local
	MediaPlayer   string      `json:"mediaPlayer"`
	DeviceInfo    *DeviceInfo `json:"deviceInfo"`
	Date          string      `json:"date"`
	TimeListening float64     `json:"timeListening"`
	StartTime     float64     `json:"startTime"`
	CurrentTime   float64     `json:"currentTime"`
	StartedAt     int64       `json:"startedAt"`
	UpdatedAt     int64       `json:"updatedAt"`
	Chapters      []Chapter   `json:"chapters"`
	AudioTracks   []Track     `json:"audioTracks"`
	MediaMetadata struct {
		Title        string      `json:"title"`
		AuthorName   string      `json:"authorName"`
		NarratorName string      `json:"narratorName"`
		SeriesName   string      `json:"seriesName"`
		Genres       []string    `json:"genres"`
		Authors      []Author    `json:"authors"`
		Series       []SeriesRef `json:"series"`
	} `json:"mediaMetadata"`
}

// PlayOptions say what the browser can play.
type PlayOptions struct {
	DeviceInfo         DeviceInfo `json:"deviceInfo"`
	ForceDirectPlay    bool       `json:"forceDirectPlay"`
	ForceTranscode     bool       `json:"forceTranscode"`
	SupportedMimeTypes []string   `json:"supportedMimeTypes"`
	MediaPlayer        string     `json:"mediaPlayer"`
}

// Play opens a playback session for a book, starting at the account's position.
func (c *Client) Play(ctx context.Context, itemID string, o PlayOptions) (*Session, error) {
	var s Session
	return &s, c.call(ctx, http.MethodPost, "/api/items/"+url.PathEscape(itemID)+"/play", nil, o, &s)
}

// SyncData is what a player reports.
type SyncData struct {
	CurrentTime  float64 `json:"currentTime"`
	TimeListened float64 `json:"timeListened"`
	Duration     float64 `json:"duration"`
}

// Sync reports position and seconds listened on an open session (this moves the account's progress).
func (c *Client) Sync(ctx context.Context, sessionID string, d SyncData) error {
	return c.call(ctx, http.MethodPost, "/api/session/"+url.PathEscape(sessionID)+"/sync", nil, d, nil)
}

// Close ends an open session with a last report.
func (c *Client) Close(ctx context.Context, sessionID string, d *SyncData) error {
	var body any
	if d != nil {
		body = d
	}
	return c.call(ctx, http.MethodPost, "/api/session/"+url.PathEscape(sessionID)+"/close", nil, body, nil)
}

// SessionsPage is a page of saved listening sessions (newest first).
type SessionsPage struct {
	Total        int       `json:"total"`
	NumPages     int       `json:"numPages"`
	Page         int       `json:"page"`
	ItemsPerPage int       `json:"itemsPerPage"`
	Sessions     []Session `json:"sessions"`
}

// ListeningSessions reads the account's saved sessions, every app's, newest first.
func (c *Client) ListeningSessions(ctx context.Context, page, perPage int) (*SessionsPage, error) {
	var p SessionsPage
	return &p, c.call(ctx, http.MethodGet, "/api/me/listening-sessions", url.Values{"page": {fmt.Sprint(page)}, "itemsPerPage": {fmt.Sprint(perPage)}}, nil, &p)
}

// CoverURL is the cover path for a book (proxied by the app).
func CoverURL(itemID string, width int) (string, url.Values) {
	q := url.Values{}
	if width > 0 {
		q.Set("width", fmt.Sprint(width))
	}
	return "/api/items/" + url.PathEscape(itemID) + "/cover", q
}
