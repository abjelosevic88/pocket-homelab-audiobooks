// Package fetch provides bounded public HTTP fetching for untrusted feed,
// metadata and artwork URLs. DNS is validated at the actual connection, including
// redirects, so URL checks cannot be bypassed by DNS rebinding.
package fetch

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

var ErrAddress = errors.New("URL must use HTTP or HTTPS and an allowed network address")

func PublicIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() {
		return false
	}
	for _, prefix := range []string{"100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "2001:db8::/32", "2001::/32", "2002::/16", "64:ff9b::/96", "64:ff9b:1::/48"} {
		if netip.MustParsePrefix(prefix).Contains(ip) {
			return false
		}
	}
	return true
}
func URL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.Fragment != "" || strings.ContainsAny(raw, "\r\n") {
		return nil, ErrAddress
	}
	return u, nil
}

// privateHosts is an operator allowlist of exact hostnames for self-hosted
// providers or private feeds. It does not authorize a redirect to another host.
func Client(timeout time.Duration, privateHosts []string) *http.Client {
	allow := map[string]bool{}
	for _, h := range privateHosts {
		allow[strings.ToLower(strings.TrimSpace(h))] = true
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, ErrAddress
		}
		ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		if err != nil {
			return nil, errors.New("could not resolve remote host")
		}
		if len(ips) == 0 {
			return nil, ErrAddress
		}
		if !allow[strings.ToLower(host)] {
			for _, ip := range ips {
				if !PublicIP(ip) {
					return nil, ErrAddress
				}
			}
		}
		dial := net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
		for _, ip := range ips {
			c, e := dial.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			if e == nil {
				return c, nil
			}
			err = e
		}
		return nil, err
	}
	transport.ResponseHeaderTimeout = 15 * time.Second
	transport.MaxConnsPerHost = 4
	return &http.Client{Timeout: timeout, Transport: transport, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("too many redirects")
		}
		if _, err := URL(req.URL.String()); err != nil {
			return err
		}
		if len(via) > 0 && via[0].URL.Scheme == "https" && req.URL.Scheme != "https" {
			return errors.New("HTTPS redirect downgrade refused")
		}
		if len(via) > 0 && req.URL.Host != via[0].URL.Host {
			req.Header.Del("Authorization")
		}
		return nil
	}}
}
func Get(ctx context.Context, client *http.Client, raw, agent, authorization string, max int64) ([]byte, string, error) {
	u, err := URL(raw)
	if err != nil {
		return nil, "", err
	}
	req, err := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	if err != nil {
		return nil, "", ErrAddress
	}
	req.Header.Set("User-Agent", agent)
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, "", errors.New("remote request failed")
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return nil, "", errors.New("remote server did not return a successful response")
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, max+1))
	if err != nil {
		return nil, "", errors.New("remote response was interrupted")
	}
	if int64(len(data)) > max {
		return nil, "", errors.New("remote response exceeds size limit")
	}
	return data, res.Header.Get("Content-Type"), nil
}
