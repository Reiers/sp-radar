package probe

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPingFoCService_OK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/pdp/ping") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Server", "PDP/1.2.3 (test)")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	r := PingFoCService(context.Background(), srv.Client(), srv.URL)
	if !r.Reachable {
		t.Errorf("expected reachable, err=%q", r.Err)
	}
	if r.StatusCode != http.StatusOK {
		t.Errorf("status: got %d", r.StatusCode)
	}
	if r.ServerHeader != "PDP/1.2.3 (test)" {
		t.Errorf("server: got %q", r.ServerHeader)
	}
	if !strings.HasSuffix(r.URL, "/pdp/ping") {
		t.Errorf("URL should end with /pdp/ping: %s", r.URL)
	}
}

func TestPingFoCService_404IsStillReachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()

	r := PingFoCService(context.Background(), srv.Client(), srv.URL)
	// 404 is still a server response — reachable, just no PDP/ping endpoint.
	if !r.Reachable {
		t.Errorf("404 should still be reachable")
	}
	if r.StatusCode != http.StatusNotFound {
		t.Errorf("status: got %d", r.StatusCode)
	}
}

func TestPingFoCService_NetworkError(t *testing.T) {
	r := PingFoCService(context.Background(), nil, "http://127.0.0.1:1") // no listener
	if r.Reachable {
		t.Errorf("expected unreachable")
	}
	if r.Err == "" {
		t.Errorf("expected error message")
	}
}

func TestPingFoCService_BadURL(t *testing.T) {
	r := PingFoCService(context.Background(), nil, "not-a-url")
	if r.Reachable {
		t.Errorf("expected unreachable")
	}
	if r.Err == "" {
		t.Errorf("expected error")
	}
}

func TestPingFoCService_ContextCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	r := PingFoCService(ctx, srv.Client(), srv.URL)
	if r.Reachable {
		t.Errorf("expected unreachable")
	}
	if r.Err == "" {
		t.Errorf("expected error message")
	}
}

func TestHostnameOf(t *testing.T) {
	cases := map[string]string{
		"https://pdp.example.com/":       "pdp.example.com",
		"http://1.2.3.4:8080/foo":        "1.2.3.4",
		"https://[2001:db8::1]:443/foo":  "2001:db8::1",
		"":                               "",
	}
	for in, want := range cases {
		if got := HostnameOf(in); got != want {
			t.Errorf("HostnameOf(%q) = %q, want %q", in, got, want)
		}
	}
}
