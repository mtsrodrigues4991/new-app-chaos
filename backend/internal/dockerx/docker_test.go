package dockerx

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func frame(stream byte, payload string) []byte {
	h := make([]byte, 8)
	h[0] = stream
	binary.BigEndian.PutUint32(h[4:], uint32(len(payload)))
	return append(h, payload...)
}

func TestDemux(t *testing.T) {
	var in bytes.Buffer
	in.Write(frame(1, "linha 1\n"))
	in.Write(frame(2, "aviso\n"))
	in.Write(frame(1, "linha 2\n"))
	var out, errOut bytes.Buffer
	if err := demux(&in, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	if out.String() != "linha 1\nlinha 2\n" || errOut.String() != "aviso\n" {
		t.Fatalf("stdout=%q stderr=%q", out.String(), errOut.String())
	}
}

func TestCPUPercent(t *testing.T) {
	var s statsJSON
	s.PreCPUStats.CPUUsage.TotalUsage = 1_000
	s.CPUStats.CPUUsage.TotalUsage = 2_000
	s.PreCPUStats.SystemUsage = 10_000
	s.CPUStats.SystemUsage = 14_000
	s.CPUStats.OnlineCPUs = 4
	if got := s.cpuPercent(); got != 100 {
		t.Fatalf("cpu = %v, esperado 100 (um núcleo inteiro)", got)
	}
}
