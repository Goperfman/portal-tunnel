package sdk

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHTTPRoutesUseLongestPrefix(t *testing.T) {
	t.Parallel()

	gotPath := make(chan string, 1)
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath <- r.URL.RequestURI()
		_, _ = w.Write([]byte("api"))
	}))
	defer apiServer.Close()

	rootServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("root"))
	}))
	defer rootServer.Close()

	handler, err := NewHTTPRoutes([]HTTPRouteConfig{
		{Prefix: "/", Upstream: rootServer.URL},
		{Prefix: "/api", Upstream: apiServer.URL},
	}, "")
	if err != nil {
		t.Fatalf("NewHTTPRoutes() error = %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "https://public.example/api/users?active=true", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if got := rec.Body.String(); got != "api" {
		t.Fatalf("body = %q, want api", got)
	}
	if got := <-gotPath; got != "/users?active=true" {
		t.Fatalf("upstream path = %q, want /users?active=true", got)
	}
}

func TestHTTPRoutesRewriteResponseHeaders(t *testing.T) {
	t.Parallel()

	var upstreamURL string
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", upstreamURL+"/base/login")
		http.SetCookie(w, &http.Cookie{Name: "sid", Value: "1", Path: "/base/session"})
		w.WriteHeader(http.StatusFound)
	}))
	defer upstreamServer.Close()
	upstreamURL = upstreamServer.URL

	handler, err := NewHTTPRoutes([]HTTPRouteConfig{
		{Prefix: "/app", Upstream: upstreamURL + "/base"},
	}, "")
	if err != nil {
		t.Fatalf("NewHTTPRoutes() error = %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "http://public.example/app/dashboard", nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if got := rec.Header().Get("Location"); got != "https://public.example/app/login" {
		t.Fatalf("Location = %q, want https://public.example/app/login", got)
	}
	if got := rec.Header().Get("Set-Cookie"); !strings.Contains(got, "Path=/app/session") {
		t.Fatalf("Set-Cookie = %q, want rewritten path", got)
	}
}

func TestHTTPRoutesRejectDuplicateNormalizedPrefixes(t *testing.T) {
	t.Parallel()

	_, err := NewHTTPRoutes([]HTTPRouteConfig{
		{Prefix: "/api", Upstream: "127.0.0.1:3001"},
		{Prefix: "/api/", Upstream: "127.0.0.1:3002"},
	}, "")
	if err == nil {
		t.Fatal("NewHTTPRoutes() error = nil, want duplicate prefix error")
	}
}
