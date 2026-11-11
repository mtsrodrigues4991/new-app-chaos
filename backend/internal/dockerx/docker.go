// Package dockerx conversa com o Docker Engine para listar alvos e controlar containers.
//
// Usa a Docker Engine API direto pelo socket Unix (sem SDK), o que mantém o
// backend sem dependências pesadas. Endpoints: https://docs.docker.com/reference/api/engine/
package dockerx

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
)

// ErrNotFound indica que o container ou a imagem não existe.
var ErrNotFound = errors.New("não encontrado no Docker")

// Container é o resumo de um container exibido na tela de alvos.
type Container struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Image    string   `json:"image"`
	State    string   `json:"state"`
	Ports    []uint16 `json:"ports"`
	Networks []string `json:"networks"`
	Service  string   `json:"service,omitempty"` // nome do serviço no docker compose, se houver
}

type Client struct {
	http *http.Client // sem timeout global: logs e stats são streams longos
}

// New conecta no socket indicado por DOCKER_SOCKET (padrão /var/run/docker.sock).
func New() (*Client, error) {
	sock := os.Getenv("DOCKER_SOCKET")
	if sock == "" {
		sock = "/var/run/docker.sock"
	}
	if _, err := os.Stat(sock); err != nil {
		return nil, fmt.Errorf("socket do Docker não encontrado em %s: %w", sock, err)
	}
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", sock)
		},
	}
	return &Client{http: &http.Client{Transport: tr}}, nil
}

// request faz a chamada e devolve a resposta aberta; quem chama fecha o Body.
func (c *Client) request(ctx context.Context, method, path string, body any) (*http.Response, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://docker"+path, rd)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusNotFound {
		resp.Body.Close()
		return nil, ErrNotFound
	}
	if resp.StatusCode >= 300 && resp.StatusCode != http.StatusNotModified {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		var e struct{ Message string }
		if json.Unmarshal(msg, &e) == nil && e.Message != "" {
			return nil, fmt.Errorf("docker %s %s: %s", method, path, e.Message)
		}
		return nil, fmt.Errorf("docker %s %s: status %d", method, path, resp.StatusCode)
	}
	return resp, nil
}

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	resp, err := c.request(ctx, method, path, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}

// ListRunning lista os containers em execução, candidatos a alvo.
func (c *Client) ListRunning(ctx context.Context) ([]Container, error) {
	var raw []struct {
		ID     string            `json:"Id"`
		Names  []string          `json:"Names"`
		Image  string            `json:"Image"`
		State  string            `json:"State"`
		Labels map[string]string `json:"Labels"`
		Ports  []struct {
			PrivatePort uint16 `json:"PrivatePort"`
		} `json:"Ports"`
		NetworkSettings struct {
			Networks map[string]json.RawMessage `json:"Networks"`
		} `json:"NetworkSettings"`
	}
	if err := c.do(ctx, http.MethodGet, "/containers/json", nil, &raw); err != nil {
		return nil, err
	}
	out := make([]Container, 0, len(raw))
	for _, ct := range raw {
		if ct.Labels[LabelRole] != "" {
			continue // containers auxiliares do próprio app não são alvos
		}
		name := ct.ID
		if len(name) > 12 {
			name = name[:12]
		}
		if len(ct.Names) > 0 {
			name = strings.TrimPrefix(ct.Names[0], "/")
		}
		// O Docker lista a mesma porta uma vez para IPv4 e outra para IPv6: remove repetidas.
		seen := map[uint16]bool{}
		ports := []uint16{}
		for _, p := range ct.Ports {
			if !seen[p.PrivatePort] {
				seen[p.PrivatePort] = true
				ports = append(ports, p.PrivatePort)
			}
		}
		sort.Slice(ports, func(i, j int) bool { return ports[i] < ports[j] })
		nets := []string{}
		for n := range ct.NetworkSettings.Networks {
			nets = append(nets, n)
		}
		sort.Strings(nets)
		out = append(out, Container{
			ID: ct.ID, Name: name, Image: ct.Image, State: ct.State, Ports: ports, Networks: nets,
			Service: ct.Labels["com.docker.compose.service"],
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// LabelRole marca os containers criados pelo app (k6, injetor) para limpeza e para não listá-los como alvo.
const LabelRole = "new-app-chaos.role"

// Inspect devolve o id e as redes de um container.
func (c *Client) Inspect(ctx context.Context, idOrName string) (id string, networks []string, running bool, err error) {
	var raw struct {
		ID    string `json:"Id"`
		State struct {
			Running bool `json:"Running"`
		} `json:"State"`
		NetworkSettings struct {
			Networks map[string]json.RawMessage `json:"Networks"`
		} `json:"NetworkSettings"`
	}
	if err := c.do(ctx, http.MethodGet, "/containers/"+url.PathEscape(idOrName)+"/json", nil, &raw); err != nil {
		return "", nil, false, err
	}
	for n := range raw.NetworkSettings.Networks {
		networks = append(networks, n)
	}
	sort.Strings(networks)
	return raw.ID, networks, raw.State.Running, nil
}

// EnsureImage baixa a imagem se ela ainda não existir localmente.
func (c *Client) EnsureImage(ctx context.Context, image string) error {
	err := c.do(ctx, http.MethodGet, "/images/"+url.PathEscape(image)+"/json", nil, nil)
	if err == nil {
		return nil
	}
	if !errors.Is(err, ErrNotFound) {
		return err
	}
	name, tag := image, "latest"
	if i := strings.LastIndex(image, ":"); i > 0 && !strings.Contains(image[i:], "/") {
		name, tag = image[:i], image[i+1:]
	}
	resp, err := c.request(ctx, http.MethodPost, "/images/create?fromImage="+url.QueryEscape(name)+"&tag="+url.QueryEscape(tag), nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	// O pull responde um stream de progresso; um erro aparece como {"error": "..."}.
	dec := json.NewDecoder(resp.Body)
	for {
		var msg struct {
			Error string `json:"error"`
		}
		if err := dec.Decode(&msg); err == io.EOF {
			return nil
		} else if err != nil {
			return err
		}
		if msg.Error != "" {
			return fmt.Errorf("pull de %s: %s", image, msg.Error)
		}
	}
}

// RunSpec descreve um container auxiliar a ser criado.
type RunSpec struct {
	Name       string
	Image      string
	Entrypoint []string
	Cmd        []string
	Env        []string
	Network    string // rede a que o container se conecta
	Role       string // valor do label LabelRole
	RunID      string
}

// CreateAndStart cria e inicia um container, devolvendo o id.
func (c *Client) CreateAndStart(ctx context.Context, spec RunSpec) (string, error) {
	body := map[string]any{
		"Image":      spec.Image,
		"Entrypoint": spec.Entrypoint,
		"Cmd":        spec.Cmd,
		"Env":        spec.Env,
		"Labels":     map[string]string{LabelRole: spec.Role, "new-app-chaos.run": spec.RunID},
		"HostConfig": map[string]any{"NetworkMode": spec.Network},
	}
	var created struct {
		ID string `json:"Id"`
	}
	path := "/containers/create"
	if spec.Name != "" {
		path += "?name=" + url.QueryEscape(spec.Name)
	}
	if err := c.do(ctx, http.MethodPost, path, body, &created); err != nil {
		return "", err
	}
	if err := c.do(ctx, http.MethodPost, "/containers/"+created.ID+"/start", nil, nil); err != nil {
		_ = c.Remove(context.Background(), created.ID)
		return "", err
	}
	return created.ID, nil
}

// FollowStdout lê o stdout do container linha a linha até ele terminar ou ctx ser cancelado.
// stderr é devolvido à parte (últimas linhas) para mensagens de erro.
func (c *Client) FollowStdout(ctx context.Context, id string, onLine func([]byte)) (stderrTail string, err error) {
	resp, err := c.request(ctx, http.MethodGet, "/containers/"+id+"/logs?follow=1&stdout=1&stderr=1", nil)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	outR, outW := io.Pipe()
	var errBuf tailBuffer
	done := make(chan struct{})
	go func() {
		defer close(done)
		sc := bufio.NewScanner(outR)
		sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
		for sc.Scan() {
			onLine(sc.Bytes())
		}
		_, _ = io.Copy(io.Discard, outR)
	}()
	err = demux(resp.Body, outW, &errBuf)
	outW.Close()
	<-done
	if errors.Is(err, context.Canceled) {
		err = nil
	}
	return errBuf.String(), err
}

// demux separa o stream multiplexado do Docker (cabeçalho de 8 bytes por bloco) em stdout e stderr.
func demux(r io.Reader, stdout, stderr io.Writer) error {
	hdr := make([]byte, 8)
	for {
		if _, err := io.ReadFull(r, hdr); err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				return nil
			}
			return err
		}
		size := int64(binary.BigEndian.Uint32(hdr[4:8]))
		var w io.Writer = io.Discard
		switch hdr[0] {
		case 1:
			w = stdout
		case 2:
			w = stderr
		}
		if _, err := io.CopyN(w, r, size); err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
	}
}

// tailBuffer guarda só os últimos 4 KB escritos.
type tailBuffer struct{ b []byte }

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.b = append(t.b, p...)
	if len(t.b) > 4096 {
		t.b = t.b[len(t.b)-4096:]
	}
	return len(p), nil
}

func (t *tailBuffer) String() string { return strings.TrimSpace(string(t.b)) }

// Wait espera o container terminar e devolve o código de saída.
func (c *Client) Wait(ctx context.Context, id string) (int, error) {
	var res struct {
		StatusCode int `json:"StatusCode"`
	}
	err := c.do(ctx, http.MethodPost, "/containers/"+id+"/wait", nil, &res)
	return res.StatusCode, err
}

// Stats lê as estatísticas do container a cada ~1 s até ctx ser cancelado.
// cpu é em % (100 = um núcleo inteiro), mem em MB (sem o cache de arquivos).
func (c *Client) Stats(ctx context.Context, idOrName string, onSample func(cpu, memMB float64)) error {
	resp, err := c.request(ctx, http.MethodGet, "/containers/"+url.PathEscape(idOrName)+"/stats?stream=1", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	dec := json.NewDecoder(resp.Body)
	for {
		var s statsJSON
		if err := dec.Decode(&s); err != nil {
			if errors.Is(err, context.Canceled) || ctx.Err() != nil || err == io.EOF {
				return nil
			}
			return err
		}
		onSample(s.cpuPercent(), s.memMB())
	}
}

type statsJSON struct {
	CPUStats    cpuStats `json:"cpu_stats"`
	PreCPUStats cpuStats `json:"precpu_stats"`
	MemoryStats struct {
		Usage uint64            `json:"usage"`
		Stats map[string]uint64 `json:"stats"`
	} `json:"memory_stats"`
}

type cpuStats struct {
	CPUUsage struct {
		TotalUsage  uint64   `json:"total_usage"`
		PercpuUsage []uint64 `json:"percpu_usage"`
	} `json:"cpu_usage"`
	SystemUsage uint64 `json:"system_cpu_usage"`
	OnlineCPUs  uint32 `json:"online_cpus"`
}

// cpuPercent segue o mesmo cálculo do `docker stats`.
func (s statsJSON) cpuPercent() float64 {
	cpuDelta := float64(s.CPUStats.CPUUsage.TotalUsage) - float64(s.PreCPUStats.CPUUsage.TotalUsage)
	sysDelta := float64(s.CPUStats.SystemUsage) - float64(s.PreCPUStats.SystemUsage)
	n := float64(s.CPUStats.OnlineCPUs)
	if n == 0 {
		n = float64(len(s.CPUStats.CPUUsage.PercpuUsage))
	}
	if cpuDelta <= 0 || sysDelta <= 0 || n == 0 {
		return 0
	}
	return cpuDelta / sysDelta * n * 100
}

// memMB segue o `docker stats`: uso menos o cache de arquivos inativos.
func (s statsJSON) memMB() float64 {
	used := s.MemoryStats.Usage
	if v, ok := s.MemoryStats.Stats["inactive_file"]; ok && v < used {
		used -= v
	} else if v, ok := s.MemoryStats.Stats["total_inactive_file"]; ok && v < used {
		used -= v
	}
	return float64(used) / 1024 / 1024
}

// Kill envia um sinal ao container (padrão SIGKILL).
func (c *Client) Kill(ctx context.Context, id, signal string) error {
	if signal == "" {
		signal = "SIGKILL"
	}
	return c.do(ctx, http.MethodPost, "/containers/"+id+"/kill?signal="+url.QueryEscape(signal), nil, nil)
}

// Restart reinicia o container alvo (falha kill/restart, marco 3).
func (c *Client) Restart(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodPost, "/containers/"+id+"/restart", nil, nil)
}

// Remove apaga o container à força.
func (c *Client) Remove(ctx context.Context, id string) error {
	err := c.do(ctx, http.MethodDelete, "/containers/"+id+"?force=1", nil, nil)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	return err
}

// RemoveByRole apaga todos os containers auxiliares do app com aquele papel (limpeza ao iniciar).
func (c *Client) RemoveByRole(ctx context.Context, role string) (int, error) {
	filters := url.QueryEscape(fmt.Sprintf(`{"label":["%s=%s"]}`, LabelRole, role))
	var list []struct {
		ID string `json:"Id"`
	}
	if err := c.do(ctx, http.MethodGet, "/containers/json?all=1&filters="+filters, nil, &list); err != nil {
		return 0, err
	}
	for _, ct := range list {
		if err := c.Remove(ctx, ct.ID); err != nil {
			return 0, err
		}
	}
	return len(list), nil
}
