// Package dockerx conversa com o Docker Engine para listar alvos e controlar containers.
//
// Usa a Docker Engine API direto pelo socket Unix (sem SDK), o que mantém o
// backend sem dependências pesadas. Endpoints: https://docs.docker.com/reference/api/engine/
package dockerx

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

// Container é o resumo de um container exibido na tela de alvos.
type Container struct {
	ID    string   `json:"id"`
	Name  string   `json:"name"`
	Image string   `json:"image"`
	State string   `json:"state"`
	Ports []uint16 `json:"ports"`
}

type Client struct {
	http *http.Client
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
	return &Client{http: &http.Client{Transport: tr, Timeout: 30 * time.Second}}, nil
}

func (c *Client) do(ctx context.Context, method, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, "http://docker"+path, nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("docker %s %s: status %d", method, path, resp.StatusCode)
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

// ListRunning lista os containers em execução, candidatos a alvo.
func (c *Client) ListRunning(ctx context.Context) ([]Container, error) {
	var raw []struct {
		ID    string   `json:"Id"`
		Names []string `json:"Names"`
		Image string   `json:"Image"`
		State string   `json:"State"`
		Ports []struct {
			PrivatePort uint16 `json:"PrivatePort"`
		} `json:"Ports"`
	}
	if err := c.do(ctx, http.MethodGet, "/containers/json", &raw); err != nil {
		return nil, err
	}
	out := make([]Container, 0, len(raw))
	for _, ct := range raw {
		name := ct.ID
		if len(name) > 12 {
			name = name[:12]
		}
		if len(ct.Names) > 0 {
			name = strings.TrimPrefix(ct.Names[0], "/")
		}
		var ports []uint16
		for _, p := range ct.Ports {
			ports = append(ports, p.PrivatePort)
		}
		out = append(out, Container{ID: ct.ID, Name: name, Image: ct.Image, State: ct.State, Ports: ports})
	}
	return out, nil
}

// Restart reinicia o container alvo (falha kill/restart).
func (c *Client) Restart(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodPost, "/containers/"+id+"/restart", nil)
}

// Kill envia um sinal ao container alvo (padrão SIGKILL).
func (c *Client) Kill(ctx context.Context, id, signal string) error {
	if signal == "" {
		signal = "SIGKILL"
	}
	return c.do(ctx, http.MethodPost, "/containers/"+id+"/kill?signal="+signal, nil)
}
