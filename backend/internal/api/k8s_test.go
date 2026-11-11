package api

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/kube"
)

func TestDeploymentInfos(t *testing.T) {
	var deps []kube.Deployment
	var svcs []kube.Service
	must := func(s string, v any) {
		if err := json.Unmarshal([]byte(s), v); err != nil {
			t.Fatal(err)
		}
	}
	must(`[
	 {"metadata":{"name":"pix-api","namespace":"pix"},"spec":{"replicas":2,"template":{"metadata":{"labels":{"app":"pix-api","tier":"web"}},"spec":{"containers":[{"image":"pix-api:local"}]}}},"status":{"readyReplicas":1}},
	 {"metadata":{"name":"coredns","namespace":"kube-system"}},
	 {"metadata":{"name":"backend","namespace":"new-app-chaos"}},
	 {"metadata":{"name":"demo-api","namespace":"demo"},"spec":{"template":{"metadata":{"labels":{"app":"demo"}}}}}
	]`, &deps)
	must(`[
	 {"metadata":{"name":"pix-api","namespace":"pix"},"spec":{"selector":{"app":"pix-api"},"ports":[{"port":8080}]}},
	 {"metadata":{"name":"pix-postgres","namespace":"pix"},"spec":{"selector":{"app":"postgres"},"ports":[{"port":5432}]}},
	 {"metadata":{"name":"pix-api","namespace":"outro"},"spec":{"selector":{"app":"pix-api"},"ports":[{"port":80}]}},
	 {"metadata":{"name":"sem-selector","namespace":"pix"},"spec":{"ports":[{"port":9}]}}
	]`, &svcs)
	got := deploymentInfos(deps, svcs, "new-app-chaos")
	want := []DeploymentInfo{
		{Namespace: "demo", Name: "demo-api", Images: []string{}, URLs: []string{}},
		{Namespace: "pix", Name: "pix-api", Replicas: 2, Ready: 1, Images: []string{"pix-api:local"}, URLs: []string{"http://pix-api.pix:8080"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
}
