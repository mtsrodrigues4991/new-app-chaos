package runner

import "testing"

func TestK6Scheduling(t *testing.T) {
	t.Setenv("K6_NODE_SELECTOR", "agentpool=load")
	t.Setenv("K6_TOLERATIONS", "kubernetes.azure.com/scalesetpriority=spot:NoSchedule, dedicado:NoExecute")
	if sel := k6NodeSelector(); sel["agentpool"] != "load" || len(sel) != 1 {
		t.Fatalf("nodeSelector: %v", sel)
	}
	tol := k6Tolerations()
	if len(tol) != 2 || tol[0]["key"] != "kubernetes.azure.com/scalesetpriority" || tol[0]["value"] != "spot" || tol[0]["effect"] != "NoSchedule" ||
		tol[1]["operator"] != "Exists" || tol[1]["effect"] != "NoExecute" {
		t.Fatalf("tolerations: %v", tol)
	}
	t.Setenv("K6_NODE_SELECTOR", "")
	t.Setenv("K6_TOLERATIONS", "")
	if len(k6NodeSelector()) != 0 || len(k6Tolerations()) != 0 {
		t.Fatal("vazio deveria não agendar nada")
	}
}
