package claudeadapter

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// StartStreamProxy serves a loopback endpoint that forwards Claude API
// requests to upstream unchanged and ends each streamed response right after
// its message_stop event. Some API relays keep the SSE response open with
// pings for about 30 seconds after message_stop; the CLI waits for the body to
// end before it runs the next step, so every tool call and every turn stalled
// for that long. The proxy adds no credentials: the CLI still sends its own.
// Call stop only after the CLI has exited so no other process can take over
// the loopback port while the CLI may still send its key there.
func StartStreamProxy(upstream string) (string, func(), error) {
	target, err := url.Parse(strings.TrimRight(strings.TrimSpace(upstream), "/"))
	if err != nil || target.Host == "" || target.User != nil || (target.Scheme != "https" && target.Scheme != "http") {
		return "", nil, errors.New("invalid Claude API endpoint")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if pool := extraRootCAs(); pool != nil {
		config := &tls.Config{}
		if transport.TLSClientConfig != nil {
			config = transport.TLSClientConfig.Clone()
		}
		config.RootCAs = pool
		transport.TLSClientConfig = config
	}
	proxy := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(target)
			// Let the transport negotiate and decode compression itself so
			// the event stream stays readable for message_stop detection.
			r.Out.Header.Del("Accept-Encoding")
		},
		Transport:     transport,
		FlushInterval: -1,
		ModifyResponse: func(resp *http.Response) error {
			// A declared length must be honoured, and such a body ends anyway.
			if resp.ContentLength < 0 && strings.HasPrefix(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream") {
				resp.Body = &sseStopReader{body: resp.Body}
			}
			return nil
		},
		ErrorLog: log.New(io.Discard, "", 0),
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", nil, err
	}
	server := &http.Server{Handler: proxy, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = server.Serve(listener) }()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			_ = server.Close()
			transport.CloseIdleConnections()
		})
	}
	return "http://" + listener.Addr().String(), stop, nil
}

// extraRootCAs mirrors NODE_EXTRA_CA_CERTS, which the CLI itself honours, so
// endpoints behind a private CA keep working through the proxy.
func extraRootCAs() *x509.CertPool {
	path := os.Getenv("NODE_EXTRA_CA_CERTS")
	if path == "" {
		return nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if !pool.AppendCertsFromPEM(raw) {
		return nil
	}
	return pool
}

// sseStopReader passes an event stream through and reports EOF right after
// the event that carries message_stop.
type sseStopReader struct {
	body io.ReadCloser
	line []byte
	long bool
	stop bool
	done bool
}

func (r *sseStopReader) Read(p []byte) (int, error) {
	if r.done {
		return 0, io.EOF
	}
	n, err := r.body.Read(p)
	for i := 0; i < n; i++ {
		if p[i] != '\n' {
			// Only short lines can mark message_stop; skip buffering the rest.
			if !r.long {
				if len(r.line) < 256 {
					r.line = append(r.line, p[i])
				} else {
					r.long, r.line = true, r.line[:0]
				}
			}
			continue
		}
		line := bytes.TrimSuffix(r.line, []byte("\r"))
		long := r.long
		r.line, r.long = r.line[:0], false
		if len(line) == 0 && !long {
			if r.stop {
				r.done = true
				return i + 1, io.EOF
			}
			continue
		}
		if !long && isMessageStopLine(line) {
			r.stop = true
		}
	}
	return n, err
}

func (r *sseStopReader) Close() error { return r.body.Close() }

func isMessageStopLine(line []byte) bool {
	name, value, ok := bytes.Cut(line, []byte(":"))
	if !ok {
		return false
	}
	value = bytes.TrimSpace(value)
	switch string(name) {
	case "event":
		return string(value) == "message_stop"
	case "data":
		var event struct {
			Type string `json:"type"`
		}
		return json.Unmarshal(value, &event) == nil && event.Type == "message_stop"
	}
	return false
}
