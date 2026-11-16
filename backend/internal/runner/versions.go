package runner

import (
	"context"
	"fmt"
	"log"
	"regexp"
	"sort"
	"strings"

	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/kube"
	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/scenario"
	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/store"
)

// RevisionKey é onde o commit do código é declarado: rótulo OCI da imagem (lido do container,
// no Docker) ou anotação do pod (no Kubernetes, onde a API não mostra os rótulos da imagem).
const RevisionKey = "org.opencontainers.image.revision"

var commitRe = regexp.MustCompile(`^[0-9a-f]{7,40}$`)

// revision devolve o commit declarado, ou "" se o valor não parece um commit.
func revision(m map[string]string) string {
	v := strings.ToLower(strings.TrimSpace(m[RevisionKey]))
	if !commitRe.MatchString(v) {
		return ""
	}
	return v
}

// Versions lê a imagem do container alvo. O rótulo do commit vem da própria imagem.
func (t *dockerTarget) Versions(ctx context.Context) ([]store.Version, error) {
	img, err := t.p.docker.ContainerImage(ctx, t.id)
	if err != nil {
		return nil, fmt.Errorf("lendo a imagem do container %s: %w", t.name, err)
	}
	id := img.Digest
	if id == "" {
		id = img.ID
	}
	return []store.Version{{Role: store.RoleTarget, Name: t.name, Commit: revision(img.Labels), Images: []store.Image{{Image: img.Name, ID: id}}}}, nil
}

// Versions lê as imagens dos pods do alvo e das dependências usadas nas falhas.
func (t *k8sTarget) Versions(ctx context.Context) ([]store.Version, error) {
	v, err := t.p.version(ctx, store.RoleTarget, t.ns+"/"+t.name, t.ns, t.selector)
	if err != nil {
		return nil, err
	}
	out := []store.Version{v}
	seen := map[string]bool{v.Name: true}
	for _, d := range t.deps {
		if seen[d.Name()] {
			continue
		}
		seen[d.Name()] = true
		dep, err := t.p.dependency(ctx, d)
		if err != nil {
			return out, err
		}
		dv, err := t.p.version(ctx, store.RoleDependency, d.Name(), d.Namespace, kube.Selector(dep.MatchLabels))
		if err != nil {
			return out, err
		}
		out = append(out, dv)
	}
	return out, nil
}

// version resume as imagens em uso pelos pods de um seletor. Lê dos pods, e não do
// Deployment: no meio de uma troca de versão o Deployment já diz a imagem nova enquanto
// pods antigos ainda atendem.
func (p *k8sPlatform) version(ctx context.Context, role, name, ns, selector string) (store.Version, error) {
	v := store.Version{Role: role, Name: name, Images: []store.Image{}}
	pods, err := p.kube.ListPods(ctx, ns, selector)
	if err != nil {
		return v, fmt.Errorf("lendo os pods de %s: %w", name, err)
	}
	type key struct{ container, image, id string }
	count := map[key]int{}
	perContainer := map[string]map[key]bool{}
	commits := map[string]bool{}
	for _, pod := range pods {
		if pod.Status.Phase == "Succeeded" || pod.Status.Phase == "Failed" || pod.Metadata.DeletionTimestamp != nil {
			continue
		}
		commits[revision(pod.Metadata.Annotations)] = true
		ids := map[string]string{}
		for _, cs := range pod.Status.ContainerStatuses {
			ids[cs.Name] = cs.ImageID
		}
		for _, c := range pod.Spec.Containers {
			k := key{c.Name, c.Image, ids[c.Name]}
			count[k]++
			if perContainer[c.Name] == nil {
				perContainer[c.Name] = map[key]bool{}
			}
			perContainer[c.Name][k] = true
		}
	}
	for k, n := range count {
		v.Images = append(v.Images, store.Image{Container: k.container, Image: k.image, ID: k.id, Pods: n})
	}
	sort.Slice(v.Images, func(i, j int) bool {
		a, b := v.Images[i], v.Images[j]
		if a.Container != b.Container {
			return a.Container < b.Container
		}
		return a.Image+a.ID < b.Image+b.ID
	})
	for _, ks := range perContainer {
		if len(ks) > 1 {
			v.Mixed = true
		}
	}
	if len(commits) == 1 {
		for c := range commits {
			v.Commit = c
		}
	} else if len(commits) > 1 {
		v.Mixed = true
	}
	return v, nil
}

// versionsOf lê as versões sem deixar um erro impedir a execução: uma medição sem a versão
// registrada ainda vale, só não pode ser atribuída a um build.
func versionsOf(ctx context.Context, target Target, sc scenario.Scenario) []store.Version {
	vs, err := target.Versions(ctx)
	if err != nil {
		log.Printf("versão de %s não registrada: %v", sc.Target.Name(), err)
	}
	return vs
}
