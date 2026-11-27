package server

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strings"

	"github.com/abjelosevic88/pocket-homelab-audiobooks/internal/store"
	core "github.com/abjelosevic88/pocket-homelab-core/sdk"
)

// Pocket Core app links (docs/links.md in pocket-homelab-core), Audiobooks as the
// target: another Pocket app the person allowed in Core may act here for them
// with a pcx_ token, only within these scopes. Every route not listed in
// linkRoutes stays closed to it (403).

var linkOffers = []core.Offer{
	{Scope: "listening:read", Name: "See what you listen to", Description: "Read what you listened to (per day, history, finished books and stats) and the book you're in the middle of, for your journal, reading log and dashboards."},
	{Scope: "library:read", Name: "Look through your audiobooks", Description: "Search the audiobooks you can open and see their covers, authors and narrators, e.g. to show that you already own a book as an audiobook."},
	{Scope: "library:scan", Name: "Add new audiobooks", Description: "Have a library scanned after the app put a new book into its folder (Pocket Requests filing an audiobook you asked for), so it shows up within moments."},
}

// portable is what Pocket Move may copy out and bring in for a person
// (docs/move.md in pocket-homelab-core); Register adds move:export/import.
// The books themselves live in the library; a person's own data is what
// they listened to, finished and lined up.
var portable = &core.Portable{
	Exports: []core.PortableExport{
		{Path: "/api/v1/export.json", File: "audiobooks.json", Kind: core.KindPocket, Title: "What you listened to, the books you finished and your up next"},
	},
	Imports: []core.PortableImport{
		{Path: "/api/v1/import", Kind: core.KindPocket, ContentType: "application/json", MaxBytes: 32 << 20, Title: "Adds the listening history, finished books and up next from another box; what you already have is left alone"},
	},
}

// capabilities are what Pocket Search, Pocket Day and Pocket Hub may ask
// here (docs/capabilities.md in pocket-homelab-core): the routes are in
// capabilities.go, Register adds the pocket:search, pocket:day and
// pocket:widget offers. A quick line has nowhere to go in an audiobook
// library, so there is no capture.
var capabilities = core.Capabilities{
	Search: &core.Searchable{What: "Your audiobooks, by title, author, narrator and series", Kinds: []string{"audiobook"}, Bangs: []string{"audiobooks", "audio"}},
	Day:    &core.DayFeed{Label: "Audiobooks", One: "book", Many: "books", Unit: "min", What: "The books you listened to that day, for how long, and the ones you finished"},
	Widget: &core.Widget{Title: "Continue listening", What: "The books you are in the middle of, with the time left, then the newest books"},
}

type linkRoute struct {
	methods string
	path    *regexp.Regexp
	scope   string // "" = any linked app
}

var linkRoutes = []linkRoute{
	{"GET", regexp.MustCompile(`^/api/v1/auth/me$`), ""},
	{"GET", regexp.MustCompile(`^/api/v1/(dashboard|history|stats|finished)$`), "listening:read"},
	{"GET", regexp.MustCompile(`^/api/v1/day/\d{4}-\d{2}-\d{2}$`), "listening:read"},
	{"GET", regexp.MustCompile(`^/api/v1/cover/[0-9A-Za-z-]+$`), "listening:read"},
	{"GET", regexp.MustCompile(`^/api/v1/cover/[0-9A-Za-z-]+$`), "library:read"},
	{"GET", regexp.MustCompile(`^/api/v1/link/find$`), "library:read"},
	{"POST", regexp.MustCompile(`^/api/v1/link/scan$`), "library:scan"},
}

var errLinkScope = errors.New("outside the link's scope")

// linkAllows reports whether a link token with these scopes may make this request.
func linkAllows(scopes []string, r *http.Request) bool {
	if portable.LinkAllows(scopes, r) || capabilities.LinkAllows(scopes, r) {
		return true
	}
	for _, lr := range linkRoutes {
		if !strings.Contains(" "+lr.methods+" ", " "+r.Method+" ") || !lr.path.MatchString(r.URL.Path) {
			continue
		}
		if lr.scope == "" {
			return true
		}
		for _, sc := range scopes {
			if sc == lr.scope {
				return true
			}
		}
	}
	return false
}

// linkUser validates a pcx_ token, checks the route against its scopes and
// maps it to the person it acts for. The second value is the acting app's slug.
func (s *Server) linkUser(ctx context.Context, tok string, r *http.Request) (*store.User, string, error) {
	in, err := s.core.Introspect(ctx, tok)
	if err != nil {
		return nil, "", err
	}
	if !in.Active || !in.Delegated() || in.User == nil || in.User.Email == "" || in.App == nil || in.App.Slug == "" {
		return nil, "", http.ErrNoCookie
	}
	if !linkAllows(strings.Fields(in.Scope), r) {
		return nil, "", errLinkScope
	}
	u, err := s.coreIdentityUser(ctx, in)
	return u, in.App.Slug, err
}

const linkAppKey ctxKey = 2
