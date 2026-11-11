package api

import (
	"errors"
	"fmt"
	"net/http"
	"sort"

	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/kube"
)

// systemNamespaces não aparecem como alvos.
var systemNamespaces = map[string]bool{
	"kube-system": true, "kube-public": true, "kube-node-lease": true,
	"local-path-storage": true, "chaos-mesh": true, "observability": true,
}

// DeploymentInfo é um Deployment candidato a alvo.
type DeploymentInfo struct {
	Namespace string   `json:"namespace"`
	Name      string   `json:"name"`
	Replicas  int      `json:"replicas"` // desejadas
	Ready     int      `json:"ready"`
	Images    []string `json:"images"`
	// URLs sugeridas: os Services do namespace que apontam para os pods do Deployment.
	URLs []string `json:"urls"`
}

func (s *Server) listDeployments(w http.ResponseWriter, r *http.Request) {
	if s.Kube == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("o backend não está rodando num cluster Kubernetes"))
		return
	}
	deps, err := s.Kube.ListDeployments(r.Context())
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	svcs, err := s.Kube.ListServices(r.Context())
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, deploymentInfos(deps, svcs, s.Kube.Namespace()))
}

func deploymentInfos(deps []kube.Deployment, svcs []kube.Service, ownNS string) []DeploymentInfo {
	out := []DeploymentInfo{}
	for _, d := range deps {
		ns := d.Metadata.Namespace
		if systemNamespaces[ns] || ns == ownNS {
			continue
		}
		info := DeploymentInfo{Namespace: ns, Name: d.Metadata.Name, Ready: d.Status.ReadyReplicas, Images: []string{}, URLs: []string{}}
		if d.Spec.Replicas != nil {
			info.Replicas = *d.Spec.Replicas
		}
		for _, c := range d.Spec.Template.Spec.Containers {
			info.Images = append(info.Images, c.Image)
		}
		podLabels := d.Spec.Template.Metadata.Labels
		for _, sv := range svcs {
			if sv.Metadata.Namespace != ns || len(sv.Spec.Selector) == 0 || !subset(sv.Spec.Selector, podLabels) {
				continue
			}
			for _, p := range sv.Spec.Ports {
				info.URLs = append(info.URLs, fmt.Sprintf("http://%s.%s:%d", sv.Metadata.Name, ns, p.Port))
			}
		}
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Namespace != out[j].Namespace {
			return out[i].Namespace < out[j].Namespace
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func subset(sel, labels map[string]string) bool {
	for k, v := range sel {
		if labels[k] != v {
			return false
		}
	}
	return true
}
