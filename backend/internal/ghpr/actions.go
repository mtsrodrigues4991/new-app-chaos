package ghpr

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// Build pede ao CI do repositório alvo que construa a imagem de uma versão. Quem constrói é o
// workflow do próprio repositório (ver deploy/target-ci/nac-build.yml): as credenciais de
// build e de registry ficam lá, fora do alcance do app e do agente.
type Build struct {
	Repo     string // dono/repo
	Workflow string // arquivo do workflow, ex.: nac-build.yml
	// DispatchRef é o branch onde o workflow está (o padrão do repositório); Ref é o que será
	// construído (um commit ou o branch da correção); Tag é a tag da imagem resultante.
	DispatchRef string
	Ref         string
	Tag         string
}

var (
	workflowRe = regexp.MustCompile(`^[A-Za-z0-9_.-]+\.ya?ml$`)
	tagRe      = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$`)
	refRe      = regexp.MustCompile(`^[A-Za-z0-9_./-]{1,200}$`)
)

func (b Build) validate() error {
	switch {
	case !repoRe.MatchString(b.Repo):
		return fmt.Errorf("repositório inválido: %q", b.Repo)
	case !workflowRe.MatchString(b.Workflow):
		return fmt.Errorf("workflow inválido: %q (esperado o nome do arquivo, ex.: nac-build.yml)", b.Workflow)
	case !tagRe.MatchString(b.Tag):
		return fmt.Errorf("tag de imagem inválida: %q", b.Tag)
	case !refRe.MatchString(b.Ref) || strings.Contains(b.Ref, "..") || !refRe.MatchString(b.DispatchRef):
		return fmt.Errorf("referência inválida: %q", b.Ref)
	}
	return nil
}

// BuildRun é uma execução do workflow de build.
type BuildRun struct {
	ID         int64  `json:"id"`
	URL        string `json:"url"`
	Status     string `json:"status"`     // queued, in_progress, completed
	Conclusion string `json:"conclusion"` // success, failure, cancelled...
}

// ErrBuildFailed indica que o workflow terminou sem sucesso.
var ErrBuildFailed = errors.New("o build da imagem falhou no CI do repositório")

// BuildImage dispara o workflow e espera ele terminar. poll é o intervalo das consultas.
//
// O GitHub não devolve o id da execução ao disparar. Ela é achada pelo título: o workflow usa
// "run-name: nac-build <tag>", e a tag é única por build.
func (c *Client) BuildImage(ctx context.Context, b Build, poll time.Duration) (BuildRun, error) {
	if !c.Configured() {
		return BuildRun{}, ErrNotConfigured
	}
	if err := b.validate(); err != nil {
		return BuildRun{}, err
	}
	since := time.Now().Add(-time.Minute).UTC()
	wf := fmt.Sprintf("/repos/%s/actions/workflows/%s", b.Repo, b.Workflow)
	err := c.do(ctx, http.MethodPost, wf+"/dispatches", map[string]any{"ref": b.DispatchRef, "inputs": map[string]string{"ref": b.Ref, "tag": b.Tag}}, nil)
	if err != nil {
		var api *APIError
		if errors.As(err, &api) && api.Status == http.StatusNotFound {
			return BuildRun{}, fmt.Errorf("o repositório %s não tem o workflow %s no branch %s, ou o token não tem a permissão Actions (leitura e escrita)", b.Repo, b.Workflow, b.DispatchRef)
		}
		return BuildRun{}, fmt.Errorf("disparando o build: %w", err)
	}
	title := "nac-build " + b.Tag
	q := url.Values{"event": {"workflow_dispatch"}, "created": {">=" + since.Format(time.RFC3339)}, "per_page": {"50"}}
	var found BuildRun
	for {
		var list struct {
			Runs []struct {
				ID         int64  `json:"id"`
				HTMLURL    string `json:"html_url"`
				Title      string `json:"display_title"`
				Status     string `json:"status"`
				Conclusion string `json:"conclusion"`
			} `json:"workflow_runs"`
		}
		if err := c.do(ctx, http.MethodGet, wf+"/runs?"+q.Encode(), nil, &list); err != nil {
			return found, fmt.Errorf("acompanhando o build: %w", err)
		}
		for _, r := range list.Runs {
			if r.Title == title {
				found = BuildRun{ID: r.ID, URL: r.HTMLURL, Status: r.Status, Conclusion: r.Conclusion}
				break
			}
		}
		if found.Status == "completed" {
			if found.Conclusion != "success" {
				return found, fmt.Errorf("%w (%s): %s", ErrBuildFailed, found.Conclusion, found.URL)
			}
			return found, nil
		}
		select {
		case <-ctx.Done():
			if found.ID == 0 {
				return found, fmt.Errorf("o build não apareceu no CI de %s (o workflow precisa de \"run-name: nac-build ${{ inputs.tag }}\"): %w", b.Repo, ctx.Err())
			}
			return found, fmt.Errorf("o build não terminou a tempo (%s): %w", found.URL, ctx.Err())
		case <-time.After(poll):
		}
	}
}
