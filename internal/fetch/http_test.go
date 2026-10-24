package fetch

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestPrivateNetworksAndRedirects(t *testing.T) {
	for _, ip := range []string{"127.0.0.1", "::1", "::ffff:127.0.0.1", "10.0.0.1", "169.254.169.254", "100.111.226.27", "224.0.0.1", "2002:7f00:1::", "64:ff9b::7f00:1"} {
		if PublicIP(netip.MustParseAddr(ip)) {
			t.Fatal("unsafe address", ip)
		}
	}
	if !PublicIP(netip.MustParseAddr("1.1.1.1")) {
		t.Fatal("public blocked")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("response")) }))
	defer server.Close()
	if _, _, err := Get(context.Background(), Client(time.Second, nil), server.URL, "test", "", 100); err == nil {
		t.Fatal("loopback allowed by default")
	}
	client := Client(time.Second, []string{"127.0.0.1"})
	if b, _, err := Get(context.Background(), client, server.URL, "test", "", 100); err != nil || string(b) != "response" {
		t.Fatalf("operator host %s %v", b, err)
	}
	if _, _, err := Get(context.Background(), client, server.URL, "test", "", 3); err == nil {
		t.Fatal("unbounded body")
	}
	u, _ := url.Parse(server.URL)
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://"+strings.Replace(u.Host, "127.0.0.1", "localhost", 1), 302)
	}))
	defer redirect.Close()
	if _, _, err := Get(context.Background(), client, redirect.URL, "test", "", 100); err == nil {
		t.Fatal("redirect bypassed host allowlist")
	}
}
