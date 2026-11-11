package runner

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/chaos"
	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/chaosmesh"
	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/kube"
	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/scenario"
	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/store"
)

// Labels dos objetos que o app cria no cluster.
const (
	labelRole = "new-app-chaos/role"
)

// k8sPlatform roda no Kubernetes: o alvo é um Deployment, o k6 é um Job no namespace do
// próprio app e as falhas são recursos do Chaos Mesh criados e apagados na hora marcada.
type k8sPlatform struct {
	kube *kube.Client
	// InjectTimeout é quanto esperar o Chaos Mesh confirmar que aplicou a falha.
	InjectTimeout time.Duration
	// Poll é o intervalo das consultas de estado (pods, falhas).
	Poll time.Duration
}

func newK8sPlatform(c *kube.Client) *k8sPlatform {
	return &k8sPlatform{kube: c, InjectTimeout: 45 * time.Second, Poll: time.Second}
}

func (p *k8sPlatform) Resolve(ctx context.Context, sc scenario.Scenario) (Target, error) {
	ns, name := sc.Target.Namespace, sc.Target.Deployment
	d, err := p.kube.GetDeployment(ctx, ns, name)
	if errors.Is(err, kube.ErrNotFound) {
		return nil, fmt.Errorf("Deployment alvo %s/%s não existe", ns, name)
	}
	if err != nil {
		return nil, err
	}
	labels := d.Spec.Selector.MatchLabels
	if len(labels) == 0 {
		return nil, fmt.Errorf("Deployment %s/%s não tem selector.matchLabels; o app precisa dele para achar os pods", ns, name)
	}
	if d.Status.ReadyReplicas == 0 {
		return nil, fmt.Errorf("Deployment %s/%s não tem nenhum pod pronto", ns, name)
	}
	return &k8sTarget{p: p, ns: ns, name: name, labels: labels, selector: kube.Selector(labels)}, nil
}

// Recover apaga falhas do Chaos Mesh e Jobs do k6 deixados por execuções interrompidas.
func (p *k8sPlatform) Recover(ctx context.Context, _ []store.Run) {
	sel := chaosmesh.LabelManaged + "=" + chaosmesh.ManagedBy
	for _, k := range chaosmesh.Kinds {
		var l struct {
			Items []struct {
				Metadata kube.Meta `json:"metadata"`
			} `json:"items"`
		}
		err := p.kube.Do(ctx, "GET", fmt.Sprintf("/apis/%s/%s?labelSelector=%s", chaosmesh.Group, k.Plural, strings.ReplaceAll(sel, "/", "%2F")), nil, &l)
		if err != nil {
			if !errors.Is(err, kube.ErrNotFound) { // sem o Chaos Mesh instalado não há o que limpar
				log.Printf("recover: listando %s: %v", k.Kind, err)
			}
			continue
		}
		for _, it := range l.Items {
			if err := p.kube.Delete(ctx, chaosmesh.Path(k, it.Metadata.Namespace, it.Metadata.Name), ""); err != nil {
				log.Printf("recover: apagando %s %s/%s: %v", k.Kind, it.Metadata.Namespace, it.Metadata.Name, err)
			} else {
				log.Printf("recover: falha %s %s/%s desfeita", k.Kind, it.Metadata.Namespace, it.Metadata.Name)
			}
		}
	}
	jobs := fmt.Sprintf("/apis/batch/v1/namespaces/%s/jobs", p.kube.Namespace())
	if err := p.kube.DeleteCollection(ctx, jobs, labelRole+"="+RoleK6); err != nil {
		log.Printf("recover: apagando Jobs do k6: %v", err)
	}
}

// dependency acha os pods de um Deployment usado pelo alvo (ex.: o banco).
func (p *k8sPlatform) dependency(ctx context.Context, d scenario.Dependency) (chaosmesh.Target, error) {
	dep, err := p.kube.GetDeployment(ctx, d.Namespace, d.Deployment)
	if errors.Is(err, kube.ErrNotFound) {
		return chaosmesh.Target{}, fmt.Errorf("Deployment da dependência %s não existe", d.Name())
	}
	if err != nil {
		return chaosmesh.Target{}, err
	}
	if len(dep.Spec.Selector.MatchLabels) == 0 {
		return chaosmesh.Target{}, fmt.Errorf("Deployment da dependência %s não tem selector.matchLabels", d.Name())
	}
	return chaosmesh.Target{Namespace: d.Namespace, MatchLabels: dep.Spec.Selector.MatchLabels}, nil
}

type k8sTarget struct {
	p        *k8sPlatform
	ns, name string
	labels   map[string]string
	selector string
}

func (t *k8sTarget) PrepareLoad(ctx context.Context, image string) error { return nil } // o kubelet baixa

// Stats lê, a cada segundo, os pods do Deployment (prontos e existentes) e o consumo deles no
// metrics-server. O metrics-server atualiza a cada ~15 s: entre uma leitura e outra o valor se repete.
func (t *k8sTarget) Stats(ctx context.Context, fn func(Sample)) error {
	tick := time.NewTicker(t.p.Poll)
	defer tick.Stop()
	warned := false
	for {
		var s Sample
		pods, err := t.p.kube.ListPods(ctx, t.ns, t.selector)
		if err == nil {
			for _, pod := range pods {
				if pod.Status.Phase == "Succeeded" || pod.Status.Phase == "Failed" {
					continue
				}
				s.Total++
				if pod.Metadata.DeletionTimestamp == nil && podReady(pod) {
					s.Ready++
				}
			}
		}
		usage, merr := t.p.kube.PodMetrics(ctx, t.ns, t.selector)
		if merr != nil {
			s.NoUsage = true
			if !warned && ctx.Err() == nil {
				log.Printf("métricas de %s/%s indisponíveis (o metrics-server está instalado?): %v", t.ns, t.name, merr)
				warned = true
			}
		}
		for _, u := range usage {
			s.CPU += u.CPUCores * 100
			s.MemMB += u.MemBytes / (1 << 20)
		}
		if err == nil || merr == nil {
			fn(s)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
		}
	}
}

func podReady(p kube.Pod) bool {
	if p.Status.Phase != "Running" || len(p.Status.ContainerStatuses) == 0 {
		return false
	}
	for _, c := range p.Status.ContainerStatuses {
		if !c.Ready {
			return false
		}
	}
	return true
}

// ApplyFault cria o recurso do Chaos Mesh e espera ele confirmar a injeção.
func (t *k8sTarget) ApplyFault(ctx context.Context, runID string, f scenario.Fault) (chaos.Revert, error) {
	name := chaosmesh.Name("nac", runID, string(f.Type), fmt.Sprint(f.StartSeconds))
	where := chaosmesh.Target{Namespace: t.ns, MatchLabels: t.labels}
	var link *chaosmesh.Target
	if d := f.Dependency; d != nil {
		dep, err := t.p.dependency(ctx, *d)
		if err != nil {
			return nil, err
		}
		if f.LinkOnly {
			link = &dep // a falha fica no alvo, só no tráfego até a dependência
		} else {
			where = dep
		}
	}
	kind, obj, err := chaosmesh.Build(f, where, link, name, runID)
	if err != nil {
		return nil, err
	}
	ns := where.Namespace // o recurso fica no namespace dos pods atingidos
	c := t.p.kube
	if err := c.Create(ctx, chaosmesh.Path(kind, ns, ""), obj); err != nil {
		if errors.Is(err, kube.ErrNotFound) {
			return nil, errors.New("o Chaos Mesh não está instalado no cluster (os recursos chaos-mesh.org não existem)")
		}
		return nil, fmt.Errorf("criando %s: %w", kind.Kind, err)
	}
	path := chaosmesh.Path(kind, ns, name)
	revert := func(ctx context.Context) error {
		if err := c.Delete(ctx, path, ""); err != nil {
			return fmt.Errorf("apagando %s: %w", kind.Kind, err)
		}
		// O Chaos Mesh desfaz a falha antes de liberar o recurso (finalizer): espera sumir.
		for {
			err := c.Do(ctx, "GET", path, nil, nil)
			if errors.Is(err, kube.ErrNotFound) {
				return nil
			}
			select {
			case <-ctx.Done():
				return fmt.Errorf("o Chaos Mesh não desfez %s a tempo (confira com kubectl get %s -n %s)", name, kind.Plural, ns)
			case <-time.After(t.p.Poll / 2):
			}
		}
	}
	deadline := time.Now().Add(t.p.InjectTimeout)
	for {
		var got struct {
			Status chaosmesh.Status `json:"status"`
		}
		if err := c.Do(ctx, "GET", path, nil, &got); err != nil && !errors.Is(err, kube.ErrNotFound) {
			_ = revert(context.Background())
			return nil, fmt.Errorf("lendo %s: %w", kind.Kind, err)
		}
		ok, lastErr := got.Status.Injected()
		if ok {
			return revert, nil
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			rctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			_ = revert(rctx)
			cancel()
			msg := fmt.Sprintf("o Chaos Mesh não confirmou a falha em %s", t.p.InjectTimeout)
			if lastErr != "" {
				msg += ": " + lastErr
			}
			return nil, errors.New(msg)
		}
		time.Sleep(t.p.Poll / 2)
	}
}

func (t *k8sTarget) ProbeURL(ctx context.Context, sc scenario.Scenario) (string, error) {
	return strings.TrimRight(sc.Target.BaseURL, "/") + sc.ProbePath, nil
}

// StartK6 cria o Job do k6 no namespace do app.
func (t *k8sTarget) StartK6(ctx context.Context, runID, image, script string) (K6, error) {
	c := t.p.kube
	ns := c.Namespace()
	name := chaosmesh.Name("nac-k6", runID)
	labels := map[string]string{chaosmesh.LabelManaged: chaosmesh.ManagedBy, labelRole: RoleK6, chaosmesh.LabelRun: runID}
	var env []map[string]string
	for _, kv := range k6Env(script) {
		k, v, _ := strings.Cut(kv, "=")
		env = append(env, map[string]string{"name": k, "value": v})
	}
	job := map[string]any{
		"apiVersion": "batch/v1",
		"kind":       "Job",
		"metadata":   map[string]any{"name": name, "namespace": ns, "labels": labels},
		"spec": map[string]any{
			"backoffLimit":            0,
			"ttlSecondsAfterFinished": 600,
			"template": map[string]any{
				"metadata": map[string]any{"labels": labels},
				"spec": map[string]any{
					"restartPolicy": "Never",
					"containers": []map[string]any{{
						"name":            "k6",
						"image":           image,
						"imagePullPolicy": "IfNotPresent", // o kind-up já carrega a imagem no nó
						"command":         []string{"sh", "-c", k6Command},
						"env":             env,
						"ports":           []map[string]any{{"containerPort": dashboardPort, "name": "dashboard"}},
					}},
				},
			},
		},
	}
	k := &k8sK6{c: c, ns: ns, job: name, selector: labelRole + "=" + RoleK6 + "," + chaosmesh.LabelRun + "=" + runID, poll: t.p.Poll}
	if err := c.Create(ctx, fmt.Sprintf("/apis/batch/v1/namespaces/%s/jobs", ns), job); err != nil {
		return k, fmt.Errorf("criando o Job do k6: %w", err)
	}
	return k, nil
}

type k8sK6 struct {
	c        *kube.Client
	ns, job  string
	selector string
	poll     time.Duration
	seen     bool // o pod já existiu (sumir depois disso = foi apagado)
}

func (k *k8sK6) pod(ctx context.Context) (*kube.Pod, error) {
	pods, err := k.c.ListPods(ctx, k.ns, k.selector)
	if err != nil || len(pods) == 0 {
		return nil, err
	}
	k.seen = true
	return &pods[0], nil
}

// stuckReasons são esperas que não se resolvem sozinhas.
var stuckReasons = map[string]bool{"ErrImagePull": true, "ImagePullBackOff": true, "InvalidImageName": true, "CreateContainerConfigError": true, "CreateContainerError": true}

func (k *k8sK6) DashboardURL(ctx context.Context) (string, error) {
	p, err := k.pod(ctx)
	if err != nil || p == nil {
		return "", nil
	}
	for _, cs := range p.Status.ContainerStatuses {
		if w := cs.State.Waiting; w != nil && stuckReasons[w.Reason] {
			return "", fmt.Errorf("o pod do k6 não sobe (%s): %s", w.Reason, w.Message)
		}
	}
	if p.Status.Phase != "Running" || p.Status.PodIP == "" {
		return "", nil
	}
	return fmt.Sprintf("http://%s:%d/events", p.Status.PodIP, dashboardPort), nil
}

func (k *k8sK6) Wait(ctx context.Context) (int, error) {
	for {
		p, err := k.pod(ctx)
		switch {
		case err != nil:
			log.Printf("k6 %s: lendo o pod: %v", k.job, err)
		case p == nil && k.seen:
			return 137, nil // apagado (abort ou corte)
		case p != nil:
			for _, cs := range p.Status.ContainerStatuses {
				if t := cs.State.Terminated; t != nil {
					return t.ExitCode, nil
				}
				if w := cs.State.Waiting; w != nil && stuckReasons[w.Reason] {
					return 1, fmt.Errorf("o pod do k6 não sobe (%s): %s", w.Reason, w.Message)
				}
			}
			if p.Status.Phase == "Failed" {
				return 1, fmt.Errorf("o pod do k6 falhou: %s %s", p.Status.Reason, p.Status.Message)
			}
		}
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-time.After(k.poll):
		}
	}
}

func (k *k8sK6) Kill(ctx context.Context) error {
	pods, err := k.c.ListPods(ctx, k.ns, k.selector)
	if err != nil {
		return err
	}
	for _, p := range pods {
		if err := k.c.DeletePod(ctx, k.ns, p.Metadata.Name); err != nil && !errors.Is(err, kube.ErrNotFound) {
			return err
		}
	}
	return nil
}

func (k *k8sK6) Remove(ctx context.Context) error {
	return k.c.Delete(ctx, fmt.Sprintf("/apis/batch/v1/namespaces/%s/jobs/%s", k.ns, k.job), "Background")
}

func (k *k8sK6) LogsTail(ctx context.Context, n int) (string, error) {
	p, err := k.pod(ctx)
	if err != nil || p == nil {
		return "", err
	}
	return k.c.PodLogs(ctx, k.ns, p.Metadata.Name, n)
}
