package runner

import (
	"os"
	"strings"
)

// k6NodeSelector lê K6_NODE_SELECTOR ("agentpool=load,outra=x").
func k6NodeSelector() map[string]string {
	out := map[string]string{}
	for _, kv := range strings.Split(os.Getenv("K6_NODE_SELECTOR"), ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(kv), "=")
		if ok && k != "" {
			out[k] = v
		}
	}
	return out
}

// k6Tolerations lê K6_TOLERATIONS ("chave=valor:Efeito,chave2:Efeito"), ex.:
// "kubernetes.azure.com/scalesetpriority=spot:NoSchedule" para o nó spot da AKS.
func k6Tolerations() []map[string]string {
	var out []map[string]string
	for _, item := range strings.Split(os.Getenv("K6_TOLERATIONS"), ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		kv, effect, _ := strings.Cut(item, ":")
		t := map[string]string{"key": kv, "operator": "Exists"}
		if k, v, ok := strings.Cut(kv, "="); ok {
			t = map[string]string{"key": k, "operator": "Equal", "value": v}
		}
		if effect != "" {
			t["effect"] = effect
		}
		out = append(out, t)
	}
	return out
}
