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
}

var errLinkScope = errors.New("outside the link's scope")

// linkAllows reports whether a link token with these scopes may make this request.
func linkAllows(scopes []string, r *http.Request) bool {
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
