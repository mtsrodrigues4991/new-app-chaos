// Package kubefake é uma API do Kubernetes em memória, para testes: guarda objetos como
// JSON genérico, filtra por labelSelector de igualdade e deixa o teste reagir a criações
// (por exemplo, marcar uma falha do Chaos Mesh como injetada ou criar o pod de um Job).
package kubefake

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"time"
)

// Obj é um objeto qualquer do Kubernetes.
type Obj = map[string]any

// Ref identifica um recurso: grupo/versão ("v1" para o core, "apps/v1"...) e plural.
type Ref struct {
	GV, Plural string
}

// Server é a API falsa.
type Server struct {
	*httptest.Server
	mu    sync.Mutex
	store map[Ref]map[string]Obj // chave: namespace/nome
	// Groups conhecidos; um grupo fora daqui responde 404 (ex.: Chaos Mesh não instalado).
	Groups map[string]bool
	// OnCreate é chamado (sem o lock) depois de cada criação.
	OnCreate func(s *Server, r Ref, obj Obj)
	// Metrics devolve o consumo de um pod (cpu, memória) no formato do metrics-server.
	Metrics map[string][2]string // nome do pod -> {"250m","64Mi"}
	// Logs é o texto devolvido por pods/{nome}/log.
	Logs string
	// Requests guarda "MÉTODO caminho" de cada chamada, para conferência.
	Requests []string
}

// New sobe a API falsa com os grupos do core, apps, batch, metrics e Chaos Mesh.
func New() *Server {
	s := &Server{
		store:   map[Ref]map[string]Obj{},
		Groups:  map[string]bool{"v1": true, "apps/v1": true, "batch/v1": true, "metrics.k8s.io/v1beta1": true, "chaos-mesh.org/v1alpha1": true},
		Metrics: map[string][2]string{},
	}
	s.Server = httptest.NewServer(http.HandlerFunc(s.handle))
	return s
}

// Put grava um objeto (namespace e nome vêm do metadata).
func (s *Server) Put(r Ref, obj Obj) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.put(r, obj)
}

func (s *Server) put(r Ref, obj Obj) {
	m := meta(obj)
	if m["creationTimestamp"] == nil {
		m["creationTimestamp"] = time.Now().UTC().Format(time.RFC3339)
	}
	if s.store[r] == nil {
		s.store[r] = map[string]Obj{}
	}
	s.store[r][fmt.Sprint(m["namespace"], "/", m["name"])] = obj
}

// Get devolve um objeto (nil se não existe).
func (s *Server) Get(r Ref, ns, name string) Obj {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.store[r][ns+"/"+name]
}

// Update aplica fn a um objeto existente.
func (s *Server) Update(r Ref, ns, name string, fn func(Obj)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if o := s.store[r][ns+"/"+name]; o != nil {
		fn(o)
	}
}

// Remove apaga um objeto.
func (s *Server) Remove(r Ref, ns, name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.store[r], ns+"/"+name)
}

// List devolve os objetos de um recurso (todos os namespaces), em ordem de nome.
func (s *Server) List(r Ref) []Obj {
	s.mu.Lock()
	defer s.mu.Unlock()
	keys := make([]string, 0, len(s.store[r]))
	for k := range s.store[r] {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]Obj, 0, len(keys))
	for _, k := range keys {
		out = append(out, s.store[r][k])
	}
	return out
}

func meta(o Obj) map[string]any {
	m, _ := o["metadata"].(map[string]any)
	if m == nil {
		m = map[string]any{}
		o["metadata"] = m
	}
	return m
}

// Labels devolve os labels de um objeto.
func Labels(o Obj) map[string]string {
	out := map[string]string{}
	if l, ok := meta(o)["labels"].(map[string]any); ok {
		for k, v := range l {
			out[k] = fmt.Sprint(v)
		}
	}
	if l, ok := meta(o)["labels"].(map[string]string); ok {
		for k, v := range l {
			out[k] = v
		}
	}
	return out
}

func matches(o Obj, selector string) bool {
	if selector == "" {
		return true
	}
	l := Labels(o)
	for _, part := range strings.Split(selector, ",") {
		k, v, _ := strings.Cut(part, "=")
		if l[k] != v {
			return false
		}
	}
	return true
}

// parse separa o caminho em recurso, namespace, nome e sub-recurso.
func parse(path string) (r Ref, ns, name, sub string, ok bool) {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	switch {
	case len(parts) >= 2 && parts[0] == "api":
		r.GV, parts = parts[1], parts[2:]
	case len(parts) >= 3 && parts[0] == "apis":
		r.GV, parts = parts[1]+"/"+parts[2], parts[3:]
	default:
		return r, "", "", "", false
	}
	if len(parts) >= 2 && parts[0] == "namespaces" {
		ns, parts = parts[1], parts[2:]
	}
	if len(parts) == 0 {
		return r, "", "", "", false
	}
	r.Plural = parts[0]
	if len(parts) > 1 {
		name = parts[1]
	}
	if len(parts) > 2 {
		sub = parts[2]
	}
	return r, ns, name, sub, true
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func notFound(w http.ResponseWriter, what string) {
	writeJSON(w, http.StatusNotFound, Obj{"kind": "Status", "message": what + " not found", "reason": "NotFound"})
}

func (s *Server) handle(w http.ResponseWriter, req *http.Request) {
	s.mu.Lock()
	s.Requests = append(s.Requests, req.Method+" "+req.URL.Path)
	s.mu.Unlock()
	r, ns, name, sub, ok := parse(req.URL.Path)
	if !ok || !s.Groups[r.GV] {
		notFound(w, req.URL.Path)
		return
	}
	sel := req.URL.Query().Get("labelSelector")

	if r.GV == "metrics.k8s.io/v1beta1" && r.Plural == "pods" {
		var items []Obj
		for _, p := range s.List(Ref{"v1", "pods"}) {
			pm := meta(p)
			if pm["namespace"] != ns || !matches(p, sel) {
				continue
			}
			u, ok := s.Metrics[fmt.Sprint(pm["name"])]
			if !ok {
				continue
			}
			items = append(items, Obj{"metadata": Obj{"name": pm["name"], "namespace": ns},
				"containers": []Obj{{"name": "main", "usage": Obj{"cpu": u[0], "memory": u[1]}}}})
		}
		writeJSON(w, http.StatusOK, Obj{"items": items})
		return
	}
	if r.Plural == "pods" && sub == "log" {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, s.Logs)
		return
	}

	switch {
	case req.Method == http.MethodGet && name == "":
		items := []Obj{}
		for _, o := range s.List(r) {
			if (ns == "" || meta(o)["namespace"] == ns) && matches(o, sel) {
				items = append(items, o)
			}
		}
		writeJSON(w, http.StatusOK, Obj{"items": items})
	case req.Method == http.MethodGet:
		o := s.Get(r, ns, name)
		if o == nil {
			notFound(w, name)
			return
		}
		writeJSON(w, http.StatusOK, o)
	case req.Method == http.MethodPost && name == "":
		var o Obj
		if err := json.NewDecoder(req.Body).Decode(&o); err != nil {
			writeJSON(w, http.StatusBadRequest, Obj{"message": err.Error()})
			return
		}
		m := meta(o)
		m["namespace"] = ns
		if s.Get(r, ns, fmt.Sprint(m["name"])) != nil {
			writeJSON(w, http.StatusConflict, Obj{"message": "already exists", "reason": "AlreadyExists"})
			return
		}
		s.Put(r, o)
		if s.OnCreate != nil {
			s.OnCreate(s, r, o)
		}
		writeJSON(w, http.StatusCreated, o)
	case req.Method == http.MethodDelete && name == "":
		for _, o := range s.List(r) {
			m := meta(o)
			if m["namespace"] == ns && matches(o, sel) {
				s.Remove(r, ns, fmt.Sprint(m["name"]))
				s.cascade(r, ns, fmt.Sprint(m["name"]))
			}
		}
		writeJSON(w, http.StatusOK, Obj{"kind": "Status", "status": "Success"})
	case req.Method == http.MethodDelete:
		if s.Get(r, ns, name) == nil {
			notFound(w, name)
			return
		}
		s.Remove(r, ns, name)
		s.cascade(r, ns, name)
		writeJSON(w, http.StatusOK, Obj{"kind": "Status", "status": "Success"})
	default:
		writeJSON(w, http.StatusMethodNotAllowed, Obj{"message": "método não suportado"})
	}
}

// cascade apaga os pods de um Job apagado (como a propagação Background).
func (s *Server) cascade(r Ref, ns, name string) {
	if r != (Ref{"batch/v1", "jobs"}) {
		return
	}
	for _, p := range s.List(Ref{"v1", "pods"}) {
		if Labels(p)["job-name"] == name && meta(p)["namespace"] == ns {
			s.Remove(Ref{"v1", "pods"}, ns, fmt.Sprint(meta(p)["name"]))
		}
	}
}

// Recursos usados pelo app.
var (
	Deployments = Ref{"apps/v1", "deployments"}
	Services    = Ref{"v1", "services"}
	Pods        = Ref{"v1", "pods"}
	Jobs        = Ref{"batch/v1", "jobs"}
)

// Deployment monta um Deployment com réplicas prontas e os pods correspondentes.
func (s *Server) Deployment(ns, name string, labels map[string]string, replicas int) {
	ls := Obj{}
	for k, v := range labels {
		ls[k] = v
	}
	s.Put(Deployments, Obj{
		"metadata": Obj{"name": name, "namespace": ns},
		"spec": Obj{
			"replicas": replicas,
			"selector": Obj{"matchLabels": ls},
			"template": Obj{"metadata": Obj{"labels": ls}, "spec": Obj{"containers": []Obj{{"name": name, "image": name + ":latest", "ports": []Obj{{"containerPort": 8080}}}}}},
		},
		"status": Obj{"replicas": replicas, "readyReplicas": replicas},
	})
	for i := 0; i < replicas; i++ {
		s.Pod(ns, fmt.Sprintf("%s-%d", name, i), labels, true)
	}
}

// Pod grava um pod Running (pronto ou não).
func (s *Server) Pod(ns, name string, labels map[string]string, ready bool) {
	ls := Obj{}
	for k, v := range labels {
		ls[k] = v
	}
	s.Put(Pods, Obj{
		"metadata": Obj{"name": name, "namespace": ns, "labels": ls},
		"status": Obj{"phase": "Running", "podIP": "10.0.0.1",
			"containerStatuses": []Obj{{"name": "main", "ready": ready, "state": Obj{"running": Obj{}}}}},
	})
}
