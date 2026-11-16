// Package sandbox roda uma rodada de uma correção automática num pod isolado: monta o código
// do commit testado, aplica o patch, deixa o agente editar quando é preciso e roda o build e os
// testes do repositório. O que sai é só um diff e o resultado dos testes; publicar é outro
// passo, fora daqui (pacote ghpr).
//
// O pod não tem conta de serviço com acesso à API do cluster, roda sem root e cada passo vê só
// o que precisa: a chave da Anthropic existe apenas no passo do agente (que não executa
// comandos), e o passo que executa o build do repositório não vê credencial nenhuma.
package sandbox

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/kube"
)

// Job é uma rodada no sandbox.
type Job struct {
	ID     string // identifica a tentativa e a rodada (vira o nome do Job)
	Repo   string // dono/repo
	Commit string // commit testado: a base do patch
	// Patch é o diff a aplicar. PatchDir é a subpasta a que ele é relativo ("" = raiz do repositório).
	Patch    string
	PatchDir string
	// Task são as instruções do agente. Ele só roda se o patch não aplicar como veio ou se
	// ForceAgent estiver ligado (rodada depois de um build que falhou).
	Task       string
	ForceAgent bool
	Model      string
	// BudgetUSD é o teto de gasto do agente nesta rodada.
	BudgetUSD float64
	// TestImage e TestCommand são o build e os testes do repositório; WorkDir é a subpasta onde rodam.
	TestImage   string
	TestCommand string
	WorkDir     string
	// Timeout é o prazo da rodada inteira.
	Timeout time.Duration
}

// Result é o que a rodada produziu.
type Result struct {
	// Applied diz se o patch aplicou como veio (sem o agente).
	Applied  bool   `json:"applied"`
	ApplyErr string `json:"applyErr,omitempty"`
	// Diff é tudo que mudou sobre o commit de base, relativo à raiz do repositório.
	Diff string `json:"diff"`
	// AgentRan diz se o agente rodou; os campos seguintes vêm da saída dele.
	AgentRan    bool    `json:"agentRan"`
	AgentOutput string  `json:"agentOutput,omitempty"`
	AgentError  string  `json:"agentError,omitempty"`
	CostUSD     float64 `json:"costUsd,omitempty"`
	Turns       int     `json:"turns,omitempty"`
	// TestExit é o código de saída do build e dos testes (0 = passaram; -1 = não rodaram).
	TestExit int    `json:"testExit"`
	TestLog  string `json:"testLog,omitempty"`
}

// Runner roda uma rodada (o pod no Kubernetes; um falso nos testes).
type Runner interface {
	Run(ctx context.Context, j Job) (Result, error)
}

// Kube roda as rodadas como Jobs num namespace próprio.
type Kube struct {
	Client    *kube.Client
	Namespace string // namespace do sandbox (nunca o do app nem o de um alvo)
	Image     string // imagem do sandbox (deploy/sandbox)
	// AnthropicKey é a chave do agente; ReadToken, o token de leitura para repositórios privados.
	AnthropicKey string
	ReadToken    string
	Poll         time.Duration
}

const (
	labelManaged = "new-app-chaos/managed-by"
	labelRole    = "new-app-chaos/role"
	roleSandbox  = "sandbox"
)

func (k *Kube) poll() time.Duration {
	if k.Poll > 0 {
		return k.Poll
	}
	return 2 * time.Second
}

// Ready diz por que o sandbox não pode rodar ("" = pode).
func (k *Kube) Ready() string {
	switch {
	case k == nil || k.Client == nil:
		return "o sandbox das correções precisa do Kubernetes (suba com make kind-up)"
	case k.Namespace == "" || k.Image == "":
		return "o sandbox não está configurado (SANDBOX_NAMESPACE e SANDBOX_IMAGE)"
	case k.AnthropicKey == "":
		return "falta a ANTHROPIC_API_KEY para o agente"
	}
	return ""
}

func (j Job) validate() error {
	switch {
	case j.ID == "" || j.Repo == "" || j.Commit == "":
		return errors.New("rodada sem id, repositório ou commit")
	case strings.TrimSpace(j.Patch) == "":
		return errors.New("rodada sem patch")
	case j.TestImage == "" || strings.TrimSpace(j.TestCommand) == "":
		return errors.New("o cenário não diz como construir e testar o repositório (imagem e comando de teste)")
	}
	return nil
}

func securityContext() map[string]any {
	return map[string]any{
		"runAsNonRoot": true, "runAsUser": 1000, "runAsGroup": 1000,
		"allowPrivilegeEscalation": false,
		"capabilities":             map[string]any{"drop": []string{"ALL"}},
		"seccompProfile":           map[string]any{"type": "RuntimeDefault"},
	}
}

// objects monta o Secret da rodada e o Job.
func (k *Kube) objects(j Job) (secret, job map[string]any, name string) {
	name = "nac-fix-" + strings.ToLower(j.ID)
	labels := map[string]string{labelManaged: "new-app-chaos", labelRole: roleSandbox}
	data := map[string]string{"patch.diff": j.Patch, "task.md": j.Task, "ANTHROPIC_API_KEY": k.AnthropicKey}
	if k.ReadToken != "" {
		data["GITHUB_TOKEN"] = k.ReadToken
	}
	secret = map[string]any{
		"apiVersion": "v1", "kind": "Secret",
		"metadata":   map[string]any{"name": name, "namespace": k.Namespace, "labels": labels},
		"stringData": data,
	}
	// Cada passo monta só as chaves de que precisa.
	vol := func(vname string, keys ...string) map[string]any {
		items := make([]map[string]string, 0, len(keys))
		for _, key := range keys {
			items = append(items, map[string]string{"key": key, "path": key})
		}
		return map[string]any{"name": vname, "secret": map[string]any{"secretName": name, "items": items, "optional": false}}
	}
	checkoutKeys := []string{"patch.diff"}
	if k.ReadToken != "" {
		checkoutKeys = append(checkoutKeys, "GITHUB_TOKEN")
	}
	mounts := func(in string) []map[string]any {
		m := []map[string]any{{"name": "work", "mountPath": "/work"}, {"name": "out", "mountPath": "/out"}}
		if in != "" {
			m = append(m, map[string]any{"name": in, "mountPath": "/in", "readOnly": true})
		}
		return m
	}
	small := map[string]any{"requests": map[string]string{"cpu": "100m", "memory": "256Mi"}, "limits": map[string]string{"cpu": "1", "memory": "1Gi"}}
	build := map[string]any{"requests": map[string]string{"cpu": "500m", "memory": "1Gi"}, "limits": map[string]string{"cpu": "2", "memory": "3Gi"}}
	step := func(cname, image string, cmd []string, env map[string]string, in string, res map[string]any) map[string]any {
		e := []map[string]string{{"name": "HOME", "value": "/work/home"}}
		for _, key := range sortedKeys(env) {
			e = append(e, map[string]string{"name": key, "value": env[key]})
		}
		return map[string]any{"name": cname, "image": image, "imagePullPolicy": "IfNotPresent", "command": cmd, "env": e,
			"volumeMounts": mounts(in), "resources": res, "securityContext": securityContext()}
	}
	runAgent := "0"
	if j.ForceAgent {
		runAgent = "1"
	}
	budget := j.BudgetUSD
	if budget <= 0 {
		budget = 1
	}
	// O agente roda se foi pedido ou se o patch não aplicou como veio (decidido dentro do pod).
	agentCmd := `if [ "$(cat /out/applied 2>/dev/null)" != 1 ]; then export RUN_AGENT=1; fi; exec agent.sh`
	// O build nunca derruba o pod: o código de saída vai para um arquivo, e quem decide é o backend.
	testCmd := `mkdir -p "$HOME"; cd "/work/repo/$NAC_WORKDIR" || exit 1; if [ ! -s /out/final.diff ]; then echo "sem mudanças para testar" > /out/test.log; echo -1 > /out/test.exit; exit 0; fi; ( ` +
		j.TestCommand + ` ) > /out/test.log 2>&1; echo $? > /out/test.exit; exit 0`
	timeout := j.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Minute
	}
	job = map[string]any{
		"apiVersion": "batch/v1", "kind": "Job",
		"metadata": map[string]any{"name": name, "namespace": k.Namespace, "labels": labels},
		"spec": map[string]any{
			"backoffLimit":            0,
			"activeDeadlineSeconds":   int(timeout / time.Second),
			"ttlSecondsAfterFinished": 3600,
			"template": map[string]any{
				"metadata": map[string]any{"labels": labels},
				"spec": map[string]any{
					"restartPolicy":                "Never",
					"automountServiceAccountToken": false,
					"enableServiceLinks":           false,
					"securityContext":              map[string]any{"fsGroup": 1000},
					"initContainers": []map[string]any{
						step("checkout", k.Image, []string{"checkout.sh"}, map[string]string{"REPO": j.Repo, "COMMIT": j.Commit, "PATCH_DIR": j.PatchDir}, "in-checkout", small),
						step("agent", k.Image, []string{"sh", "-c", agentCmd}, map[string]string{"RUN_AGENT": runAgent, "MODEL": j.Model, "MAX_BUDGET_USD": strconv.FormatFloat(budget, 'f', 2, 64)}, "in-agent", small),
						step("verify", j.TestImage, []string{"sh", "-c", testCmd}, map[string]string{"NAC_WORKDIR": j.WorkDir}, "", build),
					},
					"containers": []map[string]any{
						step("report", k.Image, []string{"report.sh"}, nil, "", small),
					},
					"volumes": []map[string]any{
						{"name": "work", "emptyDir": map[string]any{"sizeLimit": "4Gi"}},
						{"name": "out", "emptyDir": map[string]any{"sizeLimit": "64Mi"}},
						vol("in-checkout", checkoutKeys...),
						vol("in-agent", "task.md", "ANTHROPIC_API_KEY"),
					},
				},
			},
		},
	}
	return secret, job, name
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}

// Run cria o Job, espera terminar, lê o resultado e apaga o Job e o Secret da rodada.
func (k *Kube) Run(ctx context.Context, j Job) (Result, error) {
	if why := k.Ready(); why != "" {
		return Result{}, errors.New(why)
	}
	if err := j.validate(); err != nil {
		return Result{}, err
	}
	secret, job, name := k.objects(j)
	c, ns := k.Client, k.Namespace
	secretPath := fmt.Sprintf("/api/v1/namespaces/%s/secrets", ns)
	jobPath := fmt.Sprintf("/apis/batch/v1/namespaces/%s/jobs", ns)
	cleanup := func() {
		bg, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = c.Delete(bg, jobPath+"/"+name, "Background")
		_ = c.Delete(bg, secretPath+"/"+name, "")
	}
	defer cleanup()
	if err := c.Create(ctx, secretPath, secret); err != nil {
		return Result{}, fmt.Errorf("criando o Secret da rodada no namespace %s: %w", ns, err)
	}
	if err := c.Create(ctx, jobPath, job); err != nil {
		return Result{}, fmt.Errorf("criando o Job do sandbox: %w", err)
	}

	selector := "job-name=" + name
	deadline := time.Now().Add(time.Duration(job["spec"].(map[string]any)["activeDeadlineSeconds"].(int))*time.Second + 2*time.Minute)
	for {
		pods, err := c.ListPods(ctx, ns, selector)
		if err == nil && len(pods) > 0 {
			p := pods[0]
			switch p.Status.Phase {
			case "Succeeded":
				logs, err := c.PodLogsSince(ctx, ns, p.Metadata.Name, "report", time.Unix(0, 0), 2000, false)
				if err != nil {
					return Result{}, fmt.Errorf("lendo o resultado do sandbox: %w", err)
				}
				return parse(logs)
			case "Failed":
				return Result{}, fmt.Errorf("o pod do sandbox falhou: %s", k.failure(ctx, p))
			}
			for _, cs := range append(p.Status.InitContainerStatuses, p.Status.ContainerStatuses...) {
				if w := cs.State.Waiting; w != nil && stuck[w.Reason] {
					return Result{}, fmt.Errorf("o pod do sandbox não sobe (%s no passo %s): %s", w.Reason, cs.Name, w.Message)
				}
			}
		}
		if time.Now().After(deadline) {
			return Result{}, errors.New("o sandbox passou do prazo da rodada")
		}
		select {
		case <-ctx.Done():
			return Result{}, ctx.Err()
		case <-time.After(k.poll()):
		}
	}
}

// stuck são esperas que não se resolvem sozinhas.
var stuck = map[string]bool{"ErrImagePull": true, "ImagePullBackOff": true, "InvalidImageName": true, "CreateContainerConfigError": true, "CreateContainerError": true}

// failure descreve qual passo do pod falhou, com o fim do log dele.
func (k *Kube) failure(ctx context.Context, p kube.Pod) string {
	if p.Status.Reason == "DeadlineExceeded" {
		return "passou do prazo da rodada"
	}
	for _, cs := range append(p.Status.InitContainerStatuses, p.Status.ContainerStatuses...) {
		if t := cs.State.Terminated; t != nil && t.ExitCode != 0 {
			logs, _ := k.Client.PodLogsSince(ctx, k.Namespace, p.Metadata.Name, cs.Name, time.Unix(0, 0), 15, false)
			return fmt.Sprintf("o passo %s saiu com código %d: %s", cs.Name, t.ExitCode, lastLine(logs, 600))
		}
	}
	return strings.TrimSpace(p.Status.Reason + " " + p.Status.Message)
}

func lastLine(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) > max {
		s = "..." + s[len(s)-max:]
	}
	return strings.ReplaceAll(s, "\n", " | ")
}

// parse lê as linhas "###NAC chave=valor" do log do passo de relatório.
func parse(logs string) (Result, error) {
	vals := map[string]string{}
	for _, line := range strings.Split(logs, "\n") {
		i := strings.Index(line, "###NAC ")
		if i < 0 {
			continue
		}
		k, v, ok := strings.Cut(strings.TrimSpace(line[i+7:]), "=")
		if ok {
			vals[k] = v
		}
	}
	if vals["done"] != "1" {
		return Result{}, errors.New("o sandbox terminou sem relatar o resultado")
	}
	b64 := func(key string) string {
		b, err := base64.StdEncoding.DecodeString(vals[key])
		if err != nil {
			return ""
		}
		return string(b)
	}
	r := Result{Applied: vals["applied"] == "1", ApplyErr: strings.TrimSpace(b64("apply_err")), Diff: b64("diff"), TestLog: b64("test_log"), AgentError: strings.TrimSpace(b64("agent_err"))}
	r.TestExit = -1
	if n, err := strconv.Atoi(strings.TrimSpace(vals["test_exit"])); err == nil {
		r.TestExit = n
	}
	if raw := b64("agent"); raw != "" {
		r.AgentRan = true
		// Saída do Claude Code com --output-format json.
		var out struct {
			Result       string  `json:"result"`
			IsError      bool    `json:"is_error"`
			Subtype      string  `json:"subtype"`
			TotalCostUSD float64 `json:"total_cost_usd"`
			NumTurns     int     `json:"num_turns"`
		}
		if err := json.Unmarshal([]byte(raw), &out); err != nil {
			r.AgentError = strings.TrimSpace(r.AgentError + " saída do agente ilegível: " + lastLine(raw, 300))
		} else {
			r.AgentOutput, r.CostUSD, r.Turns = out.Result, out.TotalCostUSD, out.NumTurns
			if out.IsError {
				r.AgentError = strings.TrimSpace(r.AgentError + " " + out.Subtype + " " + out.Result)
			}
		}
	} else if r.AgentError != "" {
		r.AgentRan = true
	}
	return r, nil
}

// Cleanup apaga Jobs e Secrets que sobraram de rodadas interrompidas (subida do backend).
func (k *Kube) Cleanup(ctx context.Context) error {
	if k == nil || k.Client == nil || k.Namespace == "" {
		return nil
	}
	sel := labelRole + "=" + roleSandbox
	if err := k.Client.DeleteCollection(ctx, fmt.Sprintf("/apis/batch/v1/namespaces/%s/jobs", k.Namespace), sel); err != nil {
		return err
	}
	return k.Client.DeleteCollection(ctx, fmt.Sprintf("/api/v1/namespaces/%s/secrets", k.Namespace), sel)
}
