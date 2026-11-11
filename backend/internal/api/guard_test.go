package api

import "testing"

func TestGuardTargets(t *testing.T) {
	t.Setenv("ALLOWED_TARGET_HOSTS", "demo, pix,.dorigao")
	g := GuardFromEnv()
	for _, ok := range []string{"http://demo-api.demo:8080", "http://pix-api.pix.svc.cluster.local:8080", "http://svc-pedido.dorigao:8080"} {
		if err := g.CheckTarget(ok); err != nil {
			t.Errorf("%s deveria passar: %v", ok, err)
		}
	}
	for _, bad := range []string{"https://example.com", "http://evildemo:80", "http://169.254.169.254", "lixo"} {
		if err := g.CheckTarget(bad); err == nil {
			t.Errorf("%s deveria ser bloqueado", bad)
		}
	}
	var none *Guard
	if none.CheckTarget("https://example.com") != nil || !none.TakeAI() {
		t.Fatal("sem guarda tudo passa")
	}
}

func TestGuardAILimit(t *testing.T) {
	g := &Guard{AIDailyLimit: 2}
	if !g.TakeAI() || !g.TakeAI() || g.TakeAI() {
		t.Fatal("limite de 2 por dia")
	}
}
