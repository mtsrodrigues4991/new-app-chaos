package ephemeral

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/kube"
	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/kube/kubefake"
	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/scenario"
)

type o = kubefake.Obj

var src = scenario.Target{Platform: scenario.PlatformKubernetes, Namespace: "pix", Deployment: "pix-api", BaseURL: "http://pix-api.pix:8080"}

// cluster: pix-api (2 réplicas, com Service) e a dependência pix-postgres (só o Service importa).
func cluster(t *testing.T) (*kubefake.Server, *Kube) {
	f := kubefake.New()
	t.Cleanup(f.Close)
	f.Put(kubefake.Deployments, o{
		"metadata": o{"name": "pix-api", "namespace": "pix"},
		"spec": o{"replicas": 2, "selector": o{"matchLabels": o{"app": "pix-api"}},
			"template": o{"metadata": o{"labels": o{"app": "pix-api"}},
				"spec": o{"serviceAccountName": "pix", "containers": []o{{"name": "pix-api", "image": "pix-api:dev",
					"env":   []o{{"name": "POSTGRES_URL", "value": "jdbc:postgresql://pix-postgres:5432/pixdb"}},
					"ports": []o{{"containerPort": 8080}}}}}}},
		"status": o{"replicas": 2, "readyReplicas": 2},
	})
	f.Put(kubefake.Services, o{"metadata": o{"name": "pix-api", "namespace": "pix"}, "spec": o{"selector": o{"app": "pix-api"}, "ports": []o{{"name": "app", "port": 8080, "targetPort": "app"}}}})
	f.Put(kubefake.Services, o{"metadata": o{"name": "pix-postgres", "namespace": "pix"}, "spec": o{"selector": o{"app": "pix-postgres"}, "ports": []o{{"port": 5432}}}})
	f.Put(kubefake.Services, o{"metadata": o{"name": "outro", "namespace": "demo"}, "spec": o{"selector": o{"app": "x"}, "ports": []o{{"port": 80}}}})
	// As cópias ficam prontas assim que são criadas.
	f.OnCreate = func(s *kubefake.Server, r kubefake.Ref, obj o) {
		if r == kubefake.Deployments {
			m := obj["metadata"].(map[string]any)
			go s.Update(kubefake.Deployments, m["namespace"].(string), m["name"].(string), func(d o) { d["status"] = o{"replicas": 2, "readyReplicas": 2} })
		}
	}
	return f, &Kube{Client: kube.NewStatic(f.URL, "", "nac"), Namespace: "efemero", PullSecret: "ghcr", Poll: 5 * time.Millisecond, ReadyTimeout: 2 * time.Second}
}

func inNS(f *kubefake.Server, r kubefake.Ref, ns string) map[string]o {
	out := map[string]o{}
	for _, x := range f.List(r) {
		m := x["metadata"].(map[string]any)
		if m["namespace"] == ns {
			out[m["name"].(string)] = x
		}
	}
	return out
}

func TestCreateCopiesAAndB(t *testing.T) {
	f, k := cluster(t)
	env, err := k.Create(context.Background(), "x1", src, "ghcr.io/d/r:base", "ghcr.io/d/r:fix")
	if err != nil {
		t.Fatal(err)
	}
	if env.A.Namespace != "efemero" || env.A.Deployment != "nac-x1-a" || env.A.BaseURL != "http://nac-x1-a.efemero:8080" || env.B.BaseURL != "http://nac-x1-b.efemero:8080" {
		t.Fatalf("alvos: %+v", env)
	}
	deps := inNS(f, kubefake.Deployments, "efemero")
	if len(deps) != 2 {
		t.Fatalf("esperava 2 Deployments no namespace efêmero: %d", len(deps))
	}
	for name, image := range map[string]string{"nac-x1-a": "ghcr.io/d/r:base", "nac-x1-b": "ghcr.io/d/r:fix"} {
		spec := deps[name]["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
		c := spec["containers"].([]any)[0].(map[string]any)
		// Só a imagem muda; a configuração do alvo (variáveis) vem junto.
		if c["image"] != image || !strings.Contains(c["env"].([]any)[0].(map[string]any)["value"].(string), "pix-postgres") {
			t.Errorf("%s: container %+v", name, c)
		}
		// A cópia não herda a conta de serviço do alvo, e usa o Secret de pull do namespace.
		if spec["serviceAccountName"] != nil || spec["automountServiceAccountToken"] != false || spec["imagePullSecrets"].([]any)[0].(map[string]any)["name"] != "ghcr" {
			t.Errorf("%s: pod %+v", name, spec)
		}
	}
	// O alvo de verdade não foi tocado.
	var orig map[string]any // pelo JSON, como os objetos criados pela API
	raw, _ := json.Marshal(f.Get(kubefake.Deployments, "pix", "pix-api"))
	json.Unmarshal(raw, &orig)
	if img := orig["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)["image"]; img != "pix-api:dev" {
		t.Fatalf("o Deployment de referência mudou: %v", img)
	}
	svcs := inNS(f, kubefake.Services, "efemero")
	// Um Service por versão e um apelido para a dependência (e nada do namespace demo).
	if len(svcs) != 3 || svcs["pix-postgres"]["spec"].(map[string]any)["externalName"] != "pix-postgres.pix.svc.cluster.local" {
		t.Fatalf("services: %v", svcs)
	}

	// Apagar tira as cópias; o alvo e as dependências de verdade ficam.
	if err := k.Delete(context.Background(), "x1"); err != nil {
		t.Fatal(err)
	}
	if n := len(inNS(f, kubefake.Deployments, "efemero")); n != 0 {
		t.Fatalf("sobraram %d Deployments", n)
	}
	left := inNS(f, kubefake.Services, "efemero")
	if len(left) != 1 || left["pix-postgres"] == nil {
		t.Fatalf("depois do Delete só fica o apelido: %v", left)
	}
	if err := k.Cleanup(context.Background()); err != nil || len(inNS(f, kubefake.Services, "efemero")) != 0 {
		t.Fatalf("o Cleanup tira também os apelidos: %v", err)
	}
	if len(inNS(f, kubefake.Services, "pix")) != 2 || f.Get(kubefake.Deployments, "pix", "pix-api") == nil {
		t.Fatal("o namespace do alvo não pode ser tocado")
	}
}

// Uma versão que não fica pronta (imagem que não baixa) dá erro com o motivo, e nada sobra.
func TestCreateFailsCleanly(t *testing.T) {
	f, k := cluster(t)
	f.OnCreate = func(s *kubefake.Server, r kubefake.Ref, obj o) {
		if r != kubefake.Deployments {
			return
		}
		m := obj["metadata"].(map[string]any)
		arm := m["labels"].(map[string]any)[labelArm].(string)
		go s.Put(kubefake.Pods, o{
			"metadata": o{"name": m["name"].(string) + "-p", "namespace": "efemero", "labels": o{labelEnv: "x2", labelArm: arm}},
			"status":   o{"phase": "Pending", "containerStatuses": []o{{"name": "pix-api", "state": o{"waiting": o{"reason": "ImagePullBackOff", "message": "pull access denied"}}}}},
		})
	}
	k.ReadyTimeout = 100 * time.Millisecond
	_, err := k.Create(context.Background(), "x2", src, "a", "b")
	if err == nil || !strings.Contains(err.Error(), "ImagePullBackOff") {
		t.Fatalf("esperava o motivo do pod: %v", err)
	}
	if n := len(inNS(f, kubefake.Deployments, "efemero")); n != 0 {
		t.Fatalf("a falha tem que apagar o que criou: %d Deployments", n)
	}
}

// Um alvo que depende de Secret, ConfigMap ou volume do namespace dele não é copiado pela metade.
func TestCreateRefusesTargetsItCannotCopy(t *testing.T) {
	f, k := cluster(t)
	f.Update(kubefake.Deployments, "pix", "pix-api", func(d o) {
		spec := d["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
		spec["volumes"] = []o{{"name": "data", "persistentVolumeClaim": o{"claimName": "pix-data"}}}
		c := spec["containers"].([]o)[0]
		c["env"] = append(c["env"].([]o), o{"name": "SENHA", "valueFrom": o{"secretKeyRef": o{"name": "s", "key": "k"}}})
	})
	_, err := k.Create(context.Background(), "x3", src, "a", "b")
	if err == nil || !strings.Contains(err.Error(), "persistentVolumeClaim") || !strings.Contains(err.Error(), "SENHA") {
		t.Fatalf("esperava recusa citando o volume e a variável: %v", err)
	}
	if len(inNS(f, kubefake.Deployments, "efemero"))+len(inNS(f, kubefake.Services, "efemero")) != 0 {
		t.Fatal("nada pode ter sido criado")
	}
	if why := (&Kube{}).Ready(); why == "" {
		t.Fatal("sem cluster não está pronto")
	}
	docker := scenario.Target{Container: "x", BaseURL: "http://x"}
	if _, err := k.Create(context.Background(), "x4", docker, "a", "b"); err == nil {
		t.Fatal("alvo no Docker não tem ambiente efêmero")
	}
}
