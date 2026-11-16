package ghpr

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// ci simula o Actions: o dispatch registra os inputs, e a execução aparece na segunda consulta
// e termina na terceira.
func ci(t *testing.T, conclusion string, hasWorkflow bool) (*httptest.Server, *map[string]any) {
	var dispatched map[string]any
	polls := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/actions/workflows/nac-build.yml/dispatches"):
			if !hasWorkflow {
				w.WriteHeader(404)
				w.Write([]byte(`{"message":"Not Found"}`))
				return
			}
			raw, _ := io.ReadAll(r.Body)
			json.Unmarshal(raw, &dispatched)
			w.WriteHeader(204)
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/actions/workflows/nac-build.yml/runs"):
			polls++
			other := `{"id":1,"display_title":"nac-build outra-tag","status":"completed","conclusion":"success","html_url":"u1"}`
			switch polls {
			case 1:
				w.Write([]byte(`{"workflow_runs":[` + other + `]}`))
			case 2:
				w.Write([]byte(`{"workflow_runs":[{"id":9,"display_title":"nac-build nac-fix-a1","status":"in_progress","html_url":"https://github.com/dono/repo/actions/runs/9"},` + other + `]}`))
			default:
				w.Write([]byte(`{"workflow_runs":[{"id":9,"display_title":"nac-build nac-fix-a1","status":"completed","conclusion":"` + conclusion + `","html_url":"https://github.com/dono/repo/actions/runs/9"}]}`))
			}
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(s.Close)
	return s, &dispatched
}

func build() Build {
	return Build{Repo: "dono/repo", Workflow: "nac-build.yml", DispatchRef: "master", Ref: "nac/fix-a1", Tag: "nac-fix-a1"}
}

func TestBuildImage(t *testing.T) {
	s, dispatched := ci(t, "success", true)
	c := &Client{Token: "tok", APIBase: s.URL}
	run, err := c.BuildImage(context.Background(), build(), time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	// Acha a execução certa pelo título, e não a de outra tag.
	if run.ID != 9 || run.Conclusion != "success" {
		t.Fatalf("execução: %+v", run)
	}
	// O workflow é disparado no branch padrão, e o que ele constrói vai nos inputs.
	in := (*dispatched)["inputs"].(map[string]any)
	if (*dispatched)["ref"] != "master" || in["ref"] != "nac/fix-a1" || in["tag"] != "nac-fix-a1" {
		t.Fatalf("dispatch: %+v", *dispatched)
	}
}

func TestBuildImageFailures(t *testing.T) {
	s, _ := ci(t, "failure", true)
	_, err := (&Client{Token: "tok", APIBase: s.URL}).BuildImage(context.Background(), build(), time.Millisecond)
	if !errors.Is(err, ErrBuildFailed) || !strings.Contains(err.Error(), "actions/runs/9") {
		t.Fatalf("build que falha: %v", err)
	}
	s2, _ := ci(t, "success", false)
	_, err = (&Client{Token: "tok", APIBase: s2.URL}).BuildImage(context.Background(), build(), time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "não tem o workflow nac-build.yml") {
		t.Fatalf("repositório sem o workflow: %v", err)
	}
	for name, change := range map[string]func(*Build){
		"tag com espaço":       func(b *Build) { b.Tag = "a b" },
		"workflow com caminho": func(b *Build) { b.Workflow = "../x.yml" },
		"ref com ..":           func(b *Build) { b.Ref = "a/../b" },
	} {
		b := build()
		change(&b)
		if _, err := (&Client{Token: "tok", APIBase: s.URL}).BuildImage(context.Background(), b, time.Millisecond); err == nil {
			t.Errorf("%s: esperava recusa", name)
		}
	}
}
