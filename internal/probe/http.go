// Package probe contains light HTTP probes used by the FoC and SP collectors.
//
// All probes use a fixed timeout, follow redirects, capture status code and
// Server header, and never read response bodies (we don't care about the
// content, only reachability + identity hints).
package probe

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// HTTPResult is one row per HTTP probe.
type HTTPResult struct {
	URL          string
	StatusCode   int
	ServerHeader string
	Reachable    bool
	Err          string // non-nil error message if probe failed
	Duration     time.Duration
}

// PingFoCService probes the standard FoC /pdp/ping endpoint at a serviceURL.
// Reference: synapse-core/src/sp/ping.ts → GET <serviceURL>/pdp/ping
//
// Returns reachability + status + Server header. We treat any HTTP response
// as "reachable" (server is alive); only network errors count as unreachable.
func PingFoCService(ctx context.Context, client *http.Client, serviceURL string) HTTPResult {
	if client == nil {
		client = defaultClient()
	}
	out := HTTPResult{URL: serviceURL}
	if serviceURL == "" {
		out.Err = "empty serviceURL"
		return out
	}
	u, err := url.Parse(serviceURL)
	if err != nil {
		out.Err = "parse serviceURL: " + err.Error()
		return out
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		out.Err = "unsupported scheme: " + u.Scheme
		return out
	}
	pingURL := strings.TrimRight(serviceURL, "/") + "/pdp/ping"
	out.URL = pingURL

	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pingURL, nil)
	if err != nil {
		out.Err = err.Error()
		return out
	}
	req.Header.Set("User-Agent", "filcensus/0.1 (+https://filcensus.reiers.io)")
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	out.Duration = time.Since(start)
	if err != nil {
		out.Err = unwrapErr(err)
		return out
	}
	defer func() {
		// drain & close so transport reuses the connection
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}()
	out.Reachable = true
	out.StatusCode = resp.StatusCode
	out.ServerHeader = resp.Header.Get("Server")
	return out
}

// HostnameOf returns the bare hostname of a URL, useful for DNS / GeoIP.
func HostnameOf(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

func defaultClient() *http.Client {
	return &http.Client{
		Timeout: 10 * time.Second,
		// Don't auto-follow more than 5 redirects (some FoC services proxy).
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return http.ErrUseLastResponse
			}
			return nil
		},
	}
}

func unwrapErr(err error) string {
	// Trim verbose URL prefix that net/http adds.
	msg := err.Error()
	if i := strings.Index(msg, ": "); i > 0 && i < 60 {
		msg = msg[i+2:]
	}
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	return msg
}
