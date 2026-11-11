// Package kube fala com a API do Kubernetes direto por HTTP (sem client-go), do mesmo jeito
// que o pacote dockerx fala com a Engine API do Docker: só o que o app usa.
//
// Dentro do cluster usa a conta de serviço do pod (token e CA montados pelo Kubernetes).
// Fora dele, para desenvolvimento, aceita KUBE_API_URL apontando para um `kubectl proxy`.
package kube

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

const saDir = "/var/run/secrets/kubernetes.io/serviceaccount"

// ErrNotFound indica 404 na API.
var ErrNotFound = errors.New("não encontrado no Kubernetes")

// Client chama a API do Kubernetes.
type Client struct {
	Base      string // ex.: https://10.96.0.1:443
	HTTP      *http.Client
	tokenFile string // relido a cada chamada (o Kubernetes renova o token)
	token     string
	namespace string
}

// New devolve um cliente para o cluster onde o backend roda, ou para KUBE_API_URL.
// Devolve nil, nil quando não há cluster (backend rodando no docker-compose).
func New() (*Client, error) {
	if u := strings.TrimRight(os.Getenv("KUBE_API_URL"), "/"); u != "" {
		return NewStatic(u, os.Getenv("KUBE_TOKEN"), envOr("POD_NAMESPACE", "default")), nil
	}
	host, port := os.Getenv("KUBERNETES_SERVICE_HOST"), os.Getenv("KUBERNETES_SERVICE_PORT")
	if host == "" {
		return nil, nil
	}
	ca, err := os.ReadFile(saDir + "/ca.crt")
	if err != nil {
		return nil, fmt.Errorf("lendo o CA da conta de serviço: %w", err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(ca)
	ns := os.Getenv("POD_NAMESPACE")
	if ns == "" {
		b, _ := os.ReadFile(saDir + "/namespace")
		ns = strings.TrimSpace(string(b))
	}
	if ns == "" {
		ns = "default"
	}
	return &Client{
		Base:      "https://" + net.JoinHostPort(host, port),
		HTTP:      &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}},
		tokenFile: saDir + "/token",
		namespace: ns,
	}, nil
}

// NewStatic devolve um cliente para uma URL fixa (testes e kubectl proxy).
func NewStatic(base, token, namespace string) *Client {
	return &Client{Base: strings.TrimRight(base, "/"), HTTP: &http.Client{}, token: token, namespace: namespace}
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// Namespace é o namespace do próprio backend (onde ficam os Jobs do k6).
func (c *Client) Namespace() string { return c.namespace }

// APIError é uma resposta de erro da API.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("Kubernetes respondeu %d: %s", e.Status, e.Message)
}

func (c *Client) newRequest(ctx context.Context, method, path string, body any) (*http.Request, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.Base+path, rd)
	if err != nil {
		return nil, err
	}
	if body != nil {
		ct := "application/json"
		if method == http.MethodPatch {
			ct = "application/merge-patch+json"
		}
		req.Header.Set("Content-Type", ct)
	}
	req.Header.Set("Accept", "application/json")
	tok := c.token
	if c.tokenFile != "" {
		if b, err := os.ReadFile(c.tokenFile); err == nil {
			tok = strings.TrimSpace(string(b))
		}
	}
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	return req, nil
}

// Do faz uma chamada; body (se não nil) vai como JSON e a resposta é decodificada em out.
func (c *Client) Do(ctx context.Context, method, path string, body, out any) error {
	req, err := c.newRequest(ctx, method, path, body)
	if err != nil {
		return err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("%w: %s %s", ErrNotFound, method, path)
	}
	if resp.StatusCode >= 300 {
		var st struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(data, &st)
		if st.Message == "" {
			st.Message = strings.TrimSpace(string(data))
		}
		return &APIError{Status: resp.StatusCode, Message: st.Message}
	}
	if out == nil {
		return nil
	}
	if s, ok := out.(*string); ok {
		*s = string(data)
		return nil
	}
	return json.Unmarshal(data, out)
}

// Selector transforma matchLabels em labelSelector ("a=b,c=d"), em ordem estável.
func Selector(labels map[string]string) string {
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = k + "=" + labels[k]
	}
	return strings.Join(parts, ",")
}

func q(selector string) string {
	if selector == "" {
		return ""
	}
	return "?labelSelector=" + url.QueryEscape(selector)
}

// --- objetos usados pelo app (só os campos lidos) ---

// Meta é o metadata comum.
type Meta struct {
	Name              string            `json:"name"`
	Namespace         string            `json:"namespace,omitempty"`
	Labels            map[string]string `json:"labels,omitempty"`
	CreationTimestamp time.Time         `json:"creationTimestamp,omitempty"`
	DeletionTimestamp *time.Time        `json:"deletionTimestamp,omitempty"`
}

// Deployment é um apps/v1 Deployment.
type Deployment struct {
	Metadata Meta `json:"metadata"`
	Spec     struct {
		Replicas *int `json:"replicas"`
		Selector struct {
			MatchLabels map[string]string `json:"matchLabels"`
		} `json:"selector"`
		Template struct {
			Metadata Meta `json:"metadata"`
			Spec     struct {
				Containers []struct {
					Name  string `json:"name"`
					Image string `json:"image"`
					Ports []struct {
						ContainerPort int `json:"containerPort"`
					} `json:"ports"`
				} `json:"containers"`
			} `json:"spec"`
		} `json:"template"`
	} `json:"spec"`
	Status struct {
		Replicas      int `json:"replicas"`
		ReadyReplicas int `json:"readyReplicas"`
	} `json:"status"`
}

// Service é um v1 Service.
type Service struct {
	Metadata Meta `json:"metadata"`
	Spec     struct {
		Selector map[string]string `json:"selector"`
		Ports    []struct {
			Name       string `json:"name"`
			Port       int    `json:"port"`
			TargetPort any    `json:"targetPort"`
		} `json:"ports"`
	} `json:"spec"`
}

// Pod é um v1 Pod.
type Pod struct {
	Metadata Meta `json:"metadata"`
	Spec     struct {
		NodeName string `json:"nodeName"`
	} `json:"spec"`
	Status struct {
		Phase             string `json:"phase"`
		PodIP             string `json:"podIP"`
		Reason            string `json:"reason"`
		Message           string `json:"message"`
		ContainerStatuses []struct {
			Name         string `json:"name"`
			Ready        bool   `json:"ready"`
			RestartCount int    `json:"restartCount"`
			State        struct {
				Waiting *struct {
					Reason  string `json:"reason"`
					Message string `json:"message"`
				} `json:"waiting"`
				Terminated *struct {
					ExitCode int    `json:"exitCode"`
					Reason   string `json:"reason"`
				} `json:"terminated"`
			} `json:"state"`
		} `json:"containerStatuses"`
	} `json:"status"`
}

type list[T any] struct {
	Items []T `json:"items"`
}

// ListDeployments lista os Deployments de todos os namespaces.
func (c *Client) ListDeployments(ctx context.Context) ([]Deployment, error) {
	var l list[Deployment]
	err := c.Do(ctx, http.MethodGet, "/apis/apps/v1/deployments", nil, &l)
	return l.Items, err
}

// GetDeployment busca um Deployment.
func (c *Client) GetDeployment(ctx context.Context, ns, name string) (Deployment, error) {
	var d Deployment
	err := c.Do(ctx, http.MethodGet, fmt.Sprintf("/apis/apps/v1/namespaces/%s/deployments/%s", ns, name), nil, &d)
	return d, err
}

// ListServices lista os Services de todos os namespaces.
func (c *Client) ListServices(ctx context.Context) ([]Service, error) {
	var l list[Service]
	err := c.Do(ctx, http.MethodGet, "/api/v1/services", nil, &l)
	return l.Items, err
}

// ListPods lista os pods de um namespace que casam com o seletor.
func (c *Client) ListPods(ctx context.Context, ns, selector string) ([]Pod, error) {
	var l list[Pod]
	err := c.Do(ctx, http.MethodGet, fmt.Sprintf("/api/v1/namespaces/%s/pods%s", ns, q(selector)), nil, &l)
	return l.Items, err
}

// DeletePod apaga um pod na hora (sem período de graça).
func (c *Client) DeletePod(ctx context.Context, ns, name string) error {
	return c.Do(ctx, http.MethodDelete, fmt.Sprintf("/api/v1/namespaces/%s/pods/%s", ns, name), map[string]any{"gracePeriodSeconds": 0}, nil)
}

// PodLogs devolve as últimas linhas do log de um pod.
func (c *Client) PodLogs(ctx context.Context, ns, name string, tail int) (string, error) {
	var s string
	err := c.Do(ctx, http.MethodGet, fmt.Sprintf("/api/v1/namespaces/%s/pods/%s/log?tailLines=%d", ns, name, tail), nil, &s)
	return s, err
}

// PodLogsSince devolve o log de um container desde since, com o instante de cada linha
// (timestamps=true), limitado às últimas tail linhas. previous lê a instância anterior de um
// container que reiniciou.
func (c *Client) PodLogsSince(ctx context.Context, ns, pod, container string, since time.Time, tail int, previous bool) (string, error) {
	v := url.Values{}
	v.Set("container", container)
	v.Set("timestamps", "true")
	v.Set("sinceTime", since.UTC().Format(time.RFC3339))
	v.Set("tailLines", strconv.Itoa(tail))
	if previous {
		v.Set("previous", "true")
	}
	var s string
	err := c.Do(ctx, http.MethodGet, fmt.Sprintf("/api/v1/namespaces/%s/pods/%s/log?%s", ns, pod, v.Encode()), nil, &s)
	return s, err
}

// FollowPodLogs acompanha o log de um container (follow=true, com timestamps) desde since e
// chama fn a cada linha, até o container terminar ou ctx acabar. Assim as últimas linhas de
// um pod apagado (ex.: o shutdown depois de um kill) não se perdem.
func (c *Client) FollowPodLogs(ctx context.Context, ns, pod, container string, since time.Time, fn func(line string)) error {
	v := url.Values{}
	v.Set("container", container)
	v.Set("timestamps", "true")
	v.Set("follow", "true")
	v.Set("sinceTime", since.UTC().Format(time.RFC3339))
	req, err := c.newRequest(ctx, http.MethodGet, fmt.Sprintf("/api/v1/namespaces/%s/pods/%s/log?%s", ns, pod, v.Encode()), nil)
	if err != nil {
		return err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		if resp.StatusCode == http.StatusNotFound {
			return fmt.Errorf("%w: log de %s/%s", ErrNotFound, ns, pod)
		}
		return &APIError{Status: resp.StatusCode, Message: strings.TrimSpace(string(b))}
	}
	r := bufio.NewReaderSize(resp.Body, 64<<10)
	for {
		line, err := r.ReadString('\n')
		if line = strings.TrimRight(line, "\r\n"); line != "" {
			fn(line)
		}
		if err != nil {
			if err == io.EOF || ctx.Err() != nil {
				return nil
			}
			return err
		}
	}
}

// Event é um evento do Kubernetes (core/v1).
type Event struct {
	Metadata       Meta   `json:"metadata"`
	Type           string `json:"type"`
	Reason         string `json:"reason"`
	Message        string `json:"message"`
	Count          int    `json:"count"`
	InvolvedObject struct {
		Kind string `json:"kind"`
		Name string `json:"name"`
	} `json:"involvedObject"`
	FirstTimestamp *time.Time `json:"firstTimestamp"`
	LastTimestamp  *time.Time `json:"lastTimestamp"`
	EventTime      *time.Time `json:"eventTime"`
	Series         *struct {
		Count            int        `json:"count"`
		LastObservedTime *time.Time `json:"lastObservedTime"`
	} `json:"series"`
}

// When devolve o instante mais recente do evento.
func (e Event) When() time.Time {
	for _, t := range []*time.Time{e.LastTimestamp, seriesTime(e), e.EventTime, e.FirstTimestamp} {
		if t != nil && !t.IsZero() {
			return *t
		}
	}
	return e.Metadata.CreationTimestamp
}

func seriesTime(e Event) *time.Time {
	if e.Series == nil {
		return nil
	}
	return e.Series.LastObservedTime
}

// ListEvents lista os eventos de um namespace.
func (c *Client) ListEvents(ctx context.Context, ns string) ([]Event, error) {
	var l list[Event]
	err := c.Do(ctx, http.MethodGet, fmt.Sprintf("/api/v1/namespaces/%s/events", ns), nil, &l)
	return l.Items, err
}

// PodUsage é o consumo de um pod segundo o metrics-server.
type PodUsage struct {
	Pod      string
	CPUCores float64
	MemBytes float64
}

// PodMetrics lê CPU e memória dos pods (metrics.k8s.io). Precisa do metrics-server.
func (c *Client) PodMetrics(ctx context.Context, ns, selector string) ([]PodUsage, error) {
	var l list[struct {
		Metadata   Meta `json:"metadata"`
		Containers []struct {
			Usage struct {
				CPU    string `json:"cpu"`
				Memory string `json:"memory"`
			} `json:"usage"`
		} `json:"containers"`
	}]
	if err := c.Do(ctx, http.MethodGet, fmt.Sprintf("/apis/metrics.k8s.io/v1beta1/namespaces/%s/pods%s", ns, q(selector)), nil, &l); err != nil {
		return nil, err
	}
	out := make([]PodUsage, 0, len(l.Items))
	for _, it := range l.Items {
		u := PodUsage{Pod: it.Metadata.Name}
		for _, ct := range it.Containers {
			u.CPUCores += ParseQuantity(ct.Usage.CPU)
			u.MemBytes += ParseQuantity(ct.Usage.Memory)
		}
		out = append(out, u)
	}
	return out, nil
}

// ParseQuantity converte uma quantidade do Kubernetes (250m, 123456n, 64Mi, 1G, 2) em número.
func ParseQuantity(s string) float64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	suffixes := []struct {
		s string
		m float64
	}{
		{"Ki", 1 << 10}, {"Mi", 1 << 20}, {"Gi", 1 << 30}, {"Ti", 1 << 40},
		{"n", 1e-9}, {"u", 1e-6}, {"m", 1e-3}, {"k", 1e3}, {"M", 1e6}, {"G", 1e9}, {"T", 1e12},
	}
	for _, sf := range suffixes {
		if strings.HasSuffix(s, sf.s) {
			var v float64
			if _, err := fmt.Sscanf(strings.TrimSuffix(s, sf.s), "%g", &v); err != nil {
				return 0
			}
			return v * sf.m
		}
	}
	var v float64
	if _, err := fmt.Sscanf(s, "%g", &v); err != nil {
		return 0
	}
	return v
}

// Create cria um objeto em path (a coleção, ex.: /apis/batch/v1/namespaces/x/jobs).
func (c *Client) Create(ctx context.Context, path string, obj any) error {
	return c.Do(ctx, http.MethodPost, path, obj, nil)
}

// Delete apaga um objeto. propagation "Background" apaga também os dependentes (pods do Job).
func (c *Client) Delete(ctx context.Context, path, propagation string) error {
	var body any
	if propagation != "" {
		body = map[string]any{"propagationPolicy": propagation}
	}
	err := c.Do(ctx, http.MethodDelete, path, body, nil)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	return err
}

// Patch aplica um merge patch (RFC 7386) no objeto em path.
func (c *Client) Patch(ctx context.Context, path string, patch any) error {
	return c.Do(ctx, http.MethodPatch, path, patch, nil)
}

// DeleteCollection apaga todos os objetos da coleção que casam com o seletor.
func (c *Client) DeleteCollection(ctx context.Context, path, selector string) error {
	err := c.Do(ctx, http.MethodDelete, path+q(selector), map[string]any{"propagationPolicy": "Background"}, nil)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	return err
}
