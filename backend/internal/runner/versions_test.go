package runner

import (
	"context"
	"reflect"
	"testing"

	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/kube/kubefake"
	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/scenario"
	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/store"
)

// podWith grava um pod com a imagem pedida, o id do conteúdo em uso e (opcional) o commit declarado.
func podWith(f *kubefake.Server, ns, name string, labels map[string]string, image, imageID, commit string) {
	ls := obj{}
	for k, v := range labels {
		ls[k] = v
	}
	meta := obj{"name": name, "namespace": ns, "labels": ls}
	if commit != "" {
		meta["annotations"] = obj{RevisionKey: commit}
	}
	f.Put(kubefake.Pods, obj{
		"metadata": meta,
		"spec":     obj{"containers": []obj{{"name": "app", "image": image}}},
		"status": obj{"phase": "Running", "podIP": "10.0.0.1",
			"containerStatuses": []obj{{"name": "app", "image": image, "imageID": imageID, "ready": true, "state": obj{"running": obj{}}}}},
	})
}

func versions(t *testing.T, p *k8sPlatform, sc scenario.Scenario) []store.Version {
	t.Helper()
	tg, err := p.Resolve(context.Background(), sc)
	if err != nil {
		t.Fatal(err)
	}
	vs, err := tg.Versions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return vs
}

// A execução guarda o digest e o commit do que estava rodando: do alvo e da dependência
// usada numa falha.
func TestK8sVersions(t *testing.T) {
	f, p := cluster(t)
	const commit = "d4c145f82ff2a1b2c3d4e5f60718293a4b5c6d7e"
	labels := map[string]string{"app": "api"}
	// Os dois pods criados pelo cluster() não têm imagem; estes os substituem.
	podWith(f, "shop", "api-0", labels, "acr.io/api:v2", "acr.io/api@sha256:aaa", commit)
	podWith(f, "shop", "api-1", labels, "acr.io/api:v2", "acr.io/api@sha256:aaa", commit)
	f.Deployment("shop", "db", map[string]string{"app": "db"}, 0)
	podWith(f, "shop", "db-0", map[string]string{"app": "db"}, "postgres:16-alpine", "sha256:bbb", "")

	sc := k8sScenario("shop", "api")
	dep := &scenario.Dependency{Namespace: "shop", Deployment: "db"}
	sc.Faults = []scenario.Fault{
		{ID: "f1", Type: scenario.FaultKillRestart, Dependency: dep},
		{ID: "f2", Type: scenario.FaultLatency, Dependency: dep, LinkOnly: true}, // a mesma dependência entra uma vez só
	}
	got := versions(t, p, sc)
	want := []store.Version{
		{Role: store.RoleTarget, Name: "shop/api", Commit: commit, Images: []store.Image{{Container: "app", Image: "acr.io/api:v2", ID: "acr.io/api@sha256:aaa", Pods: 2}}},
		// Imagem local sem rótulo de commit: fica o id da imagem, e o commit vazio (o app não supõe).
		{Role: store.RoleDependency, Name: "shop/db", Images: []store.Image{{Container: "app", Image: "postgres:16-alpine", ID: "sha256:bbb", Pods: 1}}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("versões:\n got %+v\nwant %+v", got, want)
	}
}

// No meio de uma troca de versão há pods das duas: a execução registra as duas e marca a
// mistura, em vez de atribuir o resultado a uma versão só.
func TestK8sVersionsMixedRollout(t *testing.T) {
	f, p := cluster(t)
	labels := map[string]string{"app": "api"}
	podWith(f, "shop", "api-0", labels, "acr.io/api:v1", "acr.io/api@sha256:aaa", "1111111")
	podWith(f, "shop", "api-1", labels, "acr.io/api:v2", "acr.io/api@sha256:bbb", "2222222")
	got := versions(t, p, k8sScenario("shop", "api"))
	if len(got) != 1 || !got[0].Mixed || got[0].Commit != "" || len(got[0].Images) != 2 {
		t.Fatalf("esperava duas imagens, mistura marcada e nenhum commit: %+v", got)
	}
	if store.TargetCommit(got) != "" {
		t.Fatal("com versões misturadas a análise não pode fixar um commit")
	}
}

func TestRevisionOnlyAcceptsCommits(t *testing.T) {
	for in, want := range map[string]string{
		"D4C145F82FF2": "d4c145f82ff2", "  abc1234 ": "abc1234", "v1.2.3": "", "main": "", "": "", "abc12": "",
	} {
		if got := revision(map[string]string{RevisionKey: in}); got != want {
			t.Errorf("revision(%q) = %q, esperado %q", in, got, want)
		}
	}
}
