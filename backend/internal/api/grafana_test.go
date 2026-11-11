package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGrafanaProxyKeepsPath(t *testing.T) {
	var got string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.RequestURI()
		io.WriteString(w, "ok")
	}))
	defer up.Close()
	h, err := GrafanaProxy(up.URL)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Grafana: h}
	rec := httptest.NewRecorder()
	s.Router().ServeHTTP(rec, httptest.NewRequest("GET", "/grafana/explore?orgId=1", nil))
	if rec.Code != 200 || got != "/grafana/explore?orgId=1" {
		t.Fatalf("code %d, upstream recebeu %q", rec.Code, got)
	}
}

func TestGrafanaProxyOff(t *testing.T) {
	rec := httptest.NewRecorder()
	(&Server{}).Router().ServeHTTP(rec, httptest.NewRequest("GET", "/grafana/", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("sem proxy deveria dar 404, deu %d", rec.Code)
	}
}

func TestGrafanaProxyBadURL(t *testing.T) {
	if _, err := GrafanaProxy("lgtm:3000"); err == nil {
		t.Fatal("esperava erro")
	}
}
