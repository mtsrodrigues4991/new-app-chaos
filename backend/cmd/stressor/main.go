// Command stressor gera carga de CPU ou memória DENTRO do container alvo.
//
// O backend copia este binário estático (sem dependências) para o alvo e o executa
// via `docker exec`. Assim a carga conta no limite de CPU/memória do próprio container,
// como o stress-ng faz nos cenários de hog do krkn, mas sem exigir nada na imagem do alvo.
//
//	stressor cpu  -load 80 -workers 2 -timeout 30s
//	stressor mem  -bytes 256m|1g|80% -timeout 30s
//	stressor stop                      # encerra os outros stressors em execução
package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func main() {
	if len(os.Args) < 2 {
		fail("uso: stressor cpu|mem|stop [opções]")
	}
	switch os.Args[1] {
	case "cpu":
		fs := flag.NewFlagSet("cpu", flag.ExitOnError)
		load := fs.Int("load", 80, "% de carga por núcleo")
		workers := fs.Int("workers", 1, "núcleos")
		timeout := fs.Duration("timeout", time.Minute, "duração")
		_ = fs.Parse(os.Args[2:])
		cpu(*load, *workers, *timeout)
	case "mem":
		fs := flag.NewFlagSet("mem", flag.ExitOnError)
		size := fs.String("bytes", "256m", "tamanho ou %")
		timeout := fs.Duration("timeout", time.Minute, "duração")
		_ = fs.Parse(os.Args[2:])
		n, err := parseSize(*size)
		if err != nil {
			fail(err.Error())
		}
		mem(n, *timeout)
	case "stop":
		fmt.Println(stopOthers())
	default:
		fail("comando desconhecido: " + os.Args[1])
	}
}

func fail(msg string) {
	fmt.Fprintln(os.Stderr, msg)
	os.Exit(2)
}

// cpu ocupa cada núcleo load% do tempo, em fatias de 100 ms.
func cpu(load, workers int, timeout time.Duration) {
	if load < 1 || load > 100 || workers < 1 {
		fail("parâmetros inválidos")
	}
	runtime.GOMAXPROCS(workers)
	deadline := time.Now().Add(timeout)
	const slice = 100 * time.Millisecond
	busy := slice * time.Duration(load) / 100
	done := make(chan struct{})
	for i := 0; i < workers; i++ {
		go func() {
			x := 1.0
			for time.Now().Before(deadline) {
				start := time.Now()
				for time.Since(start) < busy {
					for j := 0; j < 1000; j++ {
						x = x*1.0000001 + 0.5
					}
				}
				if idle := slice - time.Since(start); idle > 0 {
					time.Sleep(idle)
				}
			}
			_ = x
			done <- struct{}{}
		}()
	}
	for i := 0; i < workers; i++ {
		<-done
	}
}

// mem aloca n bytes, toca cada página (para virar memória residente) e segura até o fim.
func mem(n int64, timeout time.Duration) {
	const chunk = 16 << 20
	var blocks [][]byte
	page := os.Getpagesize()
	for left := n; left > 0; left -= chunk {
		size := int64(chunk)
		if left < size {
			size = left
		}
		b := make([]byte, size)
		for i := 0; i < len(b); i += page {
			b[i] = 1
		}
		blocks = append(blocks, b)
	}
	time.Sleep(timeout)
	runtime.KeepAlive(blocks)
}

// parseSize aceita 256m, 1g, 512k, bytes, ou N% da memória disponível para o container.
func parseSize(s string) (int64, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if strings.HasSuffix(s, "%") {
		pct, err := strconv.Atoi(strings.TrimSuffix(s, "%"))
		if err != nil || pct < 1 || pct > 100 {
			return 0, fmt.Errorf("percentual inválido: %s", s)
		}
		return availableMemory() * int64(pct) / 100, nil
	}
	mult := int64(1)
	switch {
	case strings.HasSuffix(s, "k"):
		mult = 1 << 10
	case strings.HasSuffix(s, "m"):
		mult = 1 << 20
	case strings.HasSuffix(s, "g"):
		mult = 1 << 30
	}
	n, err := strconv.ParseInt(strings.TrimRight(s, "kmg"), 10, 64)
	if err != nil || n < 1 {
		return 0, fmt.Errorf("tamanho inválido: %s", s)
	}
	return n * mult, nil
}

// availableMemory é o que ainda cabe no container: limite do cgroup menos o uso atual,
// ou a memória disponível da máquina quando o container não tem limite.
func availableMemory() int64 {
	limit, usage := readInt("/sys/fs/cgroup/memory.max"), readInt("/sys/fs/cgroup/memory.current")
	if limit <= 0 { // cgroup v1
		limit, usage = readInt("/sys/fs/cgroup/memory/memory.limit_in_bytes"), readInt("/sys/fs/cgroup/memory/memory.usage_in_bytes")
	}
	host := memAvailable()
	if limit > 0 && limit < 1<<50 && (host == 0 || limit-usage < host) {
		return limit - usage
	}
	return host
}

func readInt(path string) int64 {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	n, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
	if err != nil {
		return 0 // "max" = sem limite
	}
	return n
}

func memAvailable() int64 {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) >= 2 && fields[0] == "MemAvailable:" {
			kb, _ := strconv.ParseInt(fields[1], 10, 64)
			return kb * 1024
		}
	}
	return 0
}

// stopOthers mata os outros processos deste binário (mesmo nome de arquivo) no container.
func stopOthers() string {
	self := os.Getpid()
	me := filepath.Base(os.Args[0])
	dirs, _ := os.ReadDir("/proc")
	killed := 0
	for _, d := range dirs {
		pid, err := strconv.Atoi(d.Name())
		if err != nil || pid == self {
			continue
		}
		cmd, err := os.ReadFile("/proc/" + d.Name() + "/cmdline")
		if err != nil {
			continue
		}
		args := strings.Split(string(cmd), "\x00")
		if len(args) >= 2 && filepath.Base(args[0]) == me && (args[1] == "cpu" || args[1] == "mem") {
			if syscall.Kill(pid, syscall.SIGKILL) == nil {
				killed++
			}
		}
	}
	return fmt.Sprintf("%d stressor(s) encerrado(s)", killed)
}
