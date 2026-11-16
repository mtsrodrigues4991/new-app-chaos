// Package ephemeral sobe, para um experimento, duas cópias do alvo (as versões A e B) num
// namespace separado dos alvos de verdade, e apaga tudo no fim. A versão candidata nunca
// substitui o Deployment de referência: o "rollback" é apagar as cópias.
//
// O namespace é fixo e criado pelo chart, e não um por experimento: criar namespaces exigiria
// dar ao backend escrita em Deployments no cluster inteiro. Aqui ele só escreve nesse
// namespace; cada experimento marca os seus objetos com um rótulo, que é por onde são apagados
// (também na subida do backend, se ele caiu no meio).
//
// As dependências do alvo (o banco, outras APIs) não são copiadas: as cópias falam com as
// mesmas, por apelidos (Services ExternalName) com os nomes que o alvo já usa.
package ephemeral

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/kube"
	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/scenario"
)

const (
	labelManaged = "new-app-chaos/managed-by"
	labelRole    = "new-app-chaos/role"
	roleEnv      = "ephemeral"
	labelEnv     = "new-app-chaos/env" // id do experimento
	labelArm     = "new-app-chaos/arm" // a ou b
	labelAlias   = "new-app-chaos/alias"
)

// Env são as duas cópias do alvo, prontas para um experimento.
type Env struct {
	ID string          `json:"id"`
	A  scenario.Target `json:"a"`
	B  scenario.Target `json:"b"`
}

// Kube cria os ambientes num namespace dedicado.
type Kube struct {
	Client    *kube.Client
	Namespace string // namespace dos ambientes efêmeros (criado pelo chart)
	// PullSecret é o nome de um Secret de pull que já existe no namespace ("" = imagens públicas).
	PullSecret   string
	Poll         time.Duration
	ReadyTimeout time.Duration
}

// Ready diz por que o ambiente efêmero não pode ser usado ("" = pode).
func (k *Kube) Ready() string {
	switch {
	case k == nil || k.Client == nil:
		return "o ambiente efêmero precisa do Kubernetes"
	case k.Namespace == "":
		return "o ambiente efêmero não está configurado (EPHEMERAL_NAMESPACE)"
	}
	return ""
}

func (k *Kube) poll() time.Duration {
	if k.Poll > 0 {
		return k.Poll
	}
	return 2 * time.Second
}

type obj = map[string]any

func dig(o obj, path ...string) obj {
	for _, p := range path {
		next, _ := o[p].(map[string]any)
		if next == nil {
			return obj{}
		}
		o = next
	}
	return o
}

func list(v any) []any { l, _ := v.([]any); return l }

// unsupported devolve o que o pod do alvo usa e que não existe fora do namespace dele.
func unsupported(podSpec obj) []string {
	var out []string
	for _, v := range list(podSpec["volumes"]) {
		vol, _ := v.(map[string]any)
		for _, kind := range []string{"persistentVolumeClaim", "configMap", "secret", "projected", "hostPath"} {
			if vol[kind] != nil {
				out = append(out, fmt.Sprintf("volume %v (%s)", vol["name"], kind))
			}
		}
	}
	for _, group := range []string{"containers", "initContainers"} {
		for _, c := range list(podSpec[group]) {
			ct, _ := c.(map[string]any)
			if len(list(ct["envFrom"])) > 0 {
				out = append(out, fmt.Sprintf("envFrom no container %v", ct["name"]))
			}
			for _, e := range list(ct["env"]) {
				ev, _ := e.(map[string]any)
				from := dig(ev, "valueFrom")
				if from["secretKeyRef"] != nil || from["configMapKeyRef"] != nil {
					out = append(out, fmt.Sprintf("variável %v vinda de Secret ou ConfigMap", ev["name"]))
				}
			}
		}
	}
	return out
}

// Create sobe as cópias A e B do Deployment src, com as imagens dadas, e espera ficarem prontas.
// Se algo falhar, apaga o que criou.
func (k *Kube) Create(ctx context.Context, id string, src scenario.Target, imageA, imageB string) (Env, error) {
	if why := k.Ready(); why != "" {
		return Env{}, errors.New(why)
	}
	if src.EffectivePlatform() != scenario.PlatformKubernetes {
		return Env{}, errors.New("o ambiente efêmero só existe para alvos no Kubernetes")
	}
	if src.Namespace == k.Namespace {
		return Env{}, errors.New("o alvo já está no namespace dos ambientes efêmeros")
	}
	c := k.Client
	var dep obj
	if err := c.Do(ctx, "GET", fmt.Sprintf("/apis/apps/v1/namespaces/%s/deployments/%s", src.Namespace, src.Deployment), nil, &dep); err != nil {
		return Env{}, fmt.Errorf("lendo o Deployment %s: %w", src.Name(), err)
	}
	podSpec := dig(dep, "spec", "template", "spec")
	containers := list(podSpec["containers"])
	if len(containers) == 0 {
		return Env{}, fmt.Errorf("o Deployment %s não tem containers", src.Name())
	}
	if miss := unsupported(podSpec); len(miss) > 0 {
		return Env{}, fmt.Errorf("o alvo %s usa recursos do namespace dele, que as cópias não teriam: %s. O ambiente efêmero copia só Deployments autocontidos", src.Name(), strings.Join(miss, "; "))
	}
	srcLabels := map[string]string{}
	for key, v := range dig(dep, "spec", "selector", "matchLabels") {
		srcLabels[key] = fmt.Sprint(v)
	}
	svcs, err := c.ListServices(ctx)
	if err != nil {
		return Env{}, err
	}
	// Portas: as do Service que aponta para o alvo; sem Service, a primeira porta do container.
	var ports []obj
	port := 0
	aliases := []string{}
	for _, s := range svcs {
		if s.Metadata.Namespace != src.Namespace {
			continue
		}
		if len(s.Spec.Selector) > 0 && subset(s.Spec.Selector, dig(dep, "spec", "template", "metadata", "labels")) {
			for _, p := range s.Spec.Ports {
				ports = append(ports, obj{"name": p.Name, "port": p.Port, "targetPort": p.TargetPort})
				if port == 0 {
					port = p.Port
				}
			}
			continue
		}
		aliases = append(aliases, s.Metadata.Name) // dependência: vira apelido
	}
	if port == 0 {
		first, _ := containers[0].(map[string]any)
		for _, p := range list(first["ports"]) {
			pm, _ := p.(map[string]any)
			if n, ok := pm["containerPort"].(float64); ok && port == 0 {
				port = int(n)
				ports = []obj{{"port": port, "targetPort": port}}
			}
		}
	}
	if port == 0 {
		return Env{}, fmt.Errorf("não achei a porta do alvo %s (sem Service e sem containerPort)", src.Name())
	}
	sort.Strings(aliases)

	env := Env{ID: id}
	ok := false
	defer func() {
		if !ok {
			bg, cancel := context.WithTimeout(context.Background(), time.Minute)
			_ = k.Delete(bg, id)
			cancel()
		}
	}()
	base := obj{labelManaged: "new-app-chaos", labelRole: roleEnv, labelEnv: id}
	with := func(extra obj) obj {
		out := obj{}
		for key, v := range base {
			out[key] = v
		}
		for key, v := range extra {
			out[key] = v
		}
		return out
	}
	// Apelidos das dependências: o alvo continua chamando "pix-postgres", que aqui resolve para
	// o Service de verdade no namespace de origem. Um apelido que já existe (outro experimento
	// do mesmo alvo) é reaproveitado.
	for _, name := range aliases {
		svc := obj{"apiVersion": "v1", "kind": "Service",
			"metadata": obj{"name": name, "namespace": k.Namespace, "labels": obj{labelManaged: "new-app-chaos", labelRole: roleEnv, labelAlias: src.Namespace}},
			"spec":     obj{"type": "ExternalName", "externalName": fmt.Sprintf("%s.%s.svc.cluster.local", name, src.Namespace)}}
		if err := c.Create(ctx, fmt.Sprintf("/api/v1/namespaces/%s/services", k.Namespace), svc); err != nil && !conflict(err) {
			return Env{}, fmt.Errorf("criando o apelido de %s: %w", name, err)
		}
	}
	for _, arm := range []struct{ key, image string }{{"a", imageA}, {"b", imageB}} {
		name := fmt.Sprintf("nac-%s-%s", strings.ToLower(id), arm.key)
		podLabels := with(obj{labelArm: arm.key})
		spec := deepCopy(podSpec)
		cs := list(spec["containers"])
		first, _ := cs[0].(map[string]any)
		first["image"] = arm.image
		first["imagePullPolicy"] = "IfNotPresent"
		delete(spec, "nodeName")
		delete(spec, "serviceAccountName")
		delete(spec, "serviceAccount")
		spec["automountServiceAccountToken"] = false
		if k.PullSecret != "" {
			spec["imagePullSecrets"] = []obj{{"name": k.PullSecret}}
		}
		d := obj{"apiVersion": "apps/v1", "kind": "Deployment",
			"metadata": obj{"name": name, "namespace": k.Namespace, "labels": podLabels},
			"spec": obj{
				"replicas": dig(dep, "spec")["replicas"],
				"selector": obj{"matchLabels": obj{labelEnv: id, labelArm: arm.key}},
				"template": obj{"metadata": obj{"labels": podLabels}, "spec": spec},
			}}
		if err := c.Create(ctx, fmt.Sprintf("/apis/apps/v1/namespaces/%s/deployments", k.Namespace), d); err != nil {
			return Env{}, fmt.Errorf("criando a versão %s: %w", strings.ToUpper(arm.key), err)
		}
		svc := obj{"apiVersion": "v1", "kind": "Service",
			"metadata": obj{"name": name, "namespace": k.Namespace, "labels": podLabels},
			"spec":     obj{"selector": obj{labelEnv: id, labelArm: arm.key}, "ports": ports}}
		if err := c.Create(ctx, fmt.Sprintf("/api/v1/namespaces/%s/services", k.Namespace), svc); err != nil {
			return Env{}, fmt.Errorf("criando o Service da versão %s: %w", strings.ToUpper(arm.key), err)
		}
		t := scenario.Target{Platform: scenario.PlatformKubernetes, Namespace: k.Namespace, Deployment: name, BaseURL: fmt.Sprintf("http://%s.%s:%d", name, k.Namespace, port)}
		if arm.key == "a" {
			env.A = t
		} else {
			env.B = t
		}
	}
	if err := k.waitReady(ctx, env); err != nil {
		return Env{}, err
	}
	ok = true
	return env, nil
}

func conflict(err error) bool {
	var api *kube.APIError
	return errors.As(err, &api) && api.Status == 409
}

func subset(sel map[string]string, labels obj) bool {
	for key, v := range sel {
		if fmt.Sprint(labels[key]) != v {
			return false
		}
	}
	return true
}

func deepCopy(o obj) obj {
	out := obj{}
	for key, v := range o {
		out[key] = copyAny(v)
	}
	return out
}

func copyAny(v any) any {
	switch t := v.(type) {
	case map[string]any:
		return deepCopy(t)
	case []any:
		l := make([]any, len(t))
		for i := range t {
			l[i] = copyAny(t[i])
		}
		return l
	}
	return v
}

// waitReady espera as duas versões ficarem com todos os pods prontos.
func (k *Kube) waitReady(ctx context.Context, env Env) error {
	timeout := k.ReadyTimeout
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	deadline := time.Now().Add(timeout)
	for {
		pending := ""
		for _, t := range []scenario.Target{env.A, env.B} {
			d, err := k.Client.GetDeployment(ctx, t.Namespace, t.Deployment)
			want := 1
			if err == nil && d.Spec.Replicas != nil {
				want = *d.Spec.Replicas
			}
			if err != nil || d.Status.ReadyReplicas < want || want == 0 {
				pending = t.Deployment
			}
		}
		if pending == "" {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("a versão %s não ficou pronta em %s: %s", pending, timeout, k.why(ctx, pending))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(k.poll()):
		}
	}
}

// why descreve por que os pods de uma versão não ficaram prontos (imagem que não baixa, crash...).
func (k *Kube) why(ctx context.Context, deployment string) string {
	arm := deployment[len(deployment)-1:]
	id := strings.TrimSuffix(strings.TrimPrefix(deployment, "nac-"), "-"+arm)
	pods, err := k.Client.ListPods(ctx, k.Namespace, labelEnv+"="+id+","+labelArm+"="+arm)
	if err != nil || len(pods) == 0 {
		return "nenhum pod foi criado (confira a cota e os eventos do namespace)"
	}
	for _, p := range pods {
		for _, cs := range append(p.Status.InitContainerStatuses, p.Status.ContainerStatuses...) {
			if w := cs.State.Waiting; w != nil && w.Reason != "" {
				return fmt.Sprintf("%s: %s", w.Reason, w.Message)
			}
			if t := cs.State.Terminated; t != nil && t.ExitCode != 0 {
				return fmt.Sprintf("o container %s saiu com código %d", cs.Name, t.ExitCode)
			}
		}
	}
	return "os pods existem mas não passaram na sonda de prontidão"
}

// Delete apaga as cópias de um experimento. Os apelidos das dependências ficam: são
// compartilhados entre experimentos do mesmo alvo e não rodam nada.
func (k *Kube) Delete(ctx context.Context, id string) error {
	return k.deleteWhere(ctx, labelEnv+"="+id)
}

// Cleanup apaga tudo que sobrou de experimentos interrompidos (subida do backend), inclusive os apelidos.
func (k *Kube) Cleanup(ctx context.Context) error {
	if k.Ready() != "" {
		return nil
	}
	return k.deleteWhere(ctx, labelRole+"="+roleEnv)
}

func (k *Kube) deleteWhere(ctx context.Context, selector string) error {
	c := k.Client
	var errs []string
	if err := c.DeleteCollection(ctx, fmt.Sprintf("/apis/apps/v1/namespaces/%s/deployments", k.Namespace), selector); err != nil {
		errs = append(errs, err.Error())
	}
	// Services não aceitam apagar por seleção em toda versão do Kubernetes: um a um.
	svcs, err := c.ListServices(ctx)
	if err != nil {
		errs = append(errs, err.Error())
	}
	want := map[string]string{}
	for _, kv := range strings.Split(selector, ",") {
		key, v, _ := strings.Cut(kv, "=")
		want[key] = v
	}
	for _, s := range svcs {
		if s.Metadata.Namespace != k.Namespace {
			continue
		}
		match := true
		for key, v := range want {
			if s.Metadata.Labels[key] != v {
				match = false
			}
		}
		if match {
			if err := c.Delete(ctx, fmt.Sprintf("/api/v1/namespaces/%s/services/%s", k.Namespace, s.Metadata.Name), ""); err != nil {
				errs = append(errs, err.Error())
			}
		}
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}
