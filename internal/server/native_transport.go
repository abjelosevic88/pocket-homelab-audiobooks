package server

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"sync"

	"github.com/abjelosevic88/pocket-homelab-audiobooks/internal/abs"
	"github.com/abjelosevic88/pocket-homelab-audiobooks/internal/store"
)

// The existing web handlers and native clients share the same wire adapter.
// Internal calls dispatch directly to Go handlers, never to a listening socket
// or upstream server. Pipes stream media without buffering a whole book.
func (s *Server) nativeConnection(ctx context.Context, u *store.User) (*absConn, error) {
	a, err := s.catalog().Account(ctx, u.ID)
	if err != nil {
		return nil, &errNoABS{"your account has not been mapped to the native library"}
	}
	transport := nativeTransport{s: s, uid: u.ID}
	c := abs.New("http://native.invalid", "")
	c.LocalTransport = transport
	c.HTTP = &http.Client{Transport: transport}
	return &absConn{c: c, via: "native", public: s.cfg.BaseURL, key: "native|" + a.ID, user: u.Name, userID: a.ID}, nil
}

type nativeTransport struct {
	s   *Server
	uid int64
}
type nativeWriter struct {
	header  http.Header
	ready   chan *http.Response
	pipe    *io.PipeWriter
	request *http.Request
	once    sync.Once
}

func (w *nativeWriter) Header() http.Header { return w.header }
func (w *nativeWriter) WriteHeader(status int) {
	w.once.Do(func() {
		w.ready <- &http.Response{StatusCode: status, Header: w.header.Clone(), Request: w.request, ContentLength: -1}
	})
}
func (w *nativeWriter) Write(b []byte) (int, error) { w.WriteHeader(200); return w.pipe.Write(b) }
func (w *nativeWriter) Flush()                      { w.WriteHeader(200) }

type nativeBody struct {
	*io.PipeReader
	cancel context.CancelFunc
}

func (b nativeBody) Close() error { b.cancel(); return b.PipeReader.Close() }
func (t nativeTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	ctx, cancel := context.WithCancel(r.Context())
	r = r.Clone(ctx)
	reader, writer := io.Pipe()
	ready := make(chan *http.Response, 1)
	w := &nativeWriter{header: http.Header{}, ready: ready, pipe: writer, request: r}
	go func() {
		defer writer.Close()
		defer func() {
			if v := recover(); v != nil {
				t.s.log.Error("native handler panic", "error", fmt.Sprint(v))
				w.WriteHeader(http.StatusInternalServerError)
				_ = writer.CloseWithError(fmt.Errorf("native handler failed"))
			} else {
				w.WriteHeader(http.StatusOK)
			}
		}()
		t.s.nativeAPI(w, r, t.uid)
	}()
	select {
	case res := <-ready:
		res.Body = nativeBody{reader, cancel}
		return res, nil
	case <-ctx.Done():
		reader.Close()
		cancel()
		return nil, ctx.Err()
	}
}
