package ufei

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
)

func TestKubeScopeAndDeletePreconditions(t *testing.T) {
	for _, scenario := range []string{"valid", "wrong-namespace", "wrong-pod", "unassigned", "forbidden", "missing"} {
		t.Run(scenario, func(t *testing.T) {
			deletes := make(chan metav1.DeleteOptions, 1)
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/api/v1/namespaces/team":
					w.Write([]byte(`{"apiVersion":"v1","kind":"Namespace","metadata":{"name":"team","labels":{"team":"yes"}}}`))
				case "/api/v1/namespaces/team/pods/prober":
					w.Write([]byte(`{"apiVersion":"v1","kind":"Pod","metadata":{"name":"prober","labels":{"app":"ufei"}}}`))
				case "/apis/k8s.ovn.org/v1/egressips":
					if r.URL.Query().Get("fieldSelector") != "metadata.name=team-egress" {
						t.Errorf("unscoped list: %s", r.URL)
					}
					if scenario == "forbidden" {
						http.Error(w, `{"kind":"Status","apiVersion":"v1","status":"Failure","reason":"Forbidden","code":403}`, 403)
						return
					}
					e := map[string]any{"apiVersion": "k8s.ovn.org/v1", "kind": "EgressIP", "metadata": map[string]any{"name": "team-egress", "uid": "original", "resourceVersion": "42"}, "spec": map[string]any{"egressIPs": []string{"192.0.2.1"}, "namespaceSelector": map[string]any{"matchLabels": map[string]string{"team": "yes"}}, "podSelector": map[string]any{"matchLabels": map[string]string{"app": "ufei"}}}, "status": map[string]any{"items": []any{map[string]string{"egressIP": "192.0.2.1", "node": "worker"}}}}
					spec := e["spec"].(map[string]any)
					if scenario == "wrong-namespace" {
						spec["namespaceSelector"] = map[string]any{"matchExpressions": []any{map[string]any{"key": "team", "operator": "NotIn", "values": []string{"yes"}}}}
					}
					if scenario == "wrong-pod" {
						spec["podSelector"] = map[string]any{"matchLabels": map[string]string{"app": "other"}}
					}
					if scenario == "unassigned" {
						delete(e, "status")
					}
					items := []any{e}
					if scenario == "missing" {
						items = nil
					}
					json.NewEncoder(w).Encode(map[string]any{"apiVersion": "k8s.ovn.org/v1", "kind": "EgressIPList", "items": items})
				case "/apis/k8s.ovn.org/v1/egressips/team-egress":
					if r.Method != "DELETE" {
						t.Errorf("unexpected method %s", r.Method)
					}
					var opts metav1.DeleteOptions
					json.NewDecoder(r.Body).Decode(&opts)
					deletes <- opts
					w.Write([]byte(`{"kind":"Status","apiVersion":"v1","status":"Success"}`))
				default:
					t.Errorf("unexpected request %s", r.URL)
					http.NotFound(w, r)
				}
			}))
			defer s.Close()
			client, err := dynamic.NewForConfig(&rest.Config{Host: s.URL})
			if err != nil {
				t.Fatal(err)
			}
			k := &Kube{client, Config{Namespace: "team", PodName: "prober", EgressIPs: []string{"team-egress"}}}
			items, err := k.Snapshot(context.Background())
			if scenario != "valid" {
				if err == nil {
					t.Fatal("unsafe snapshot accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := k.Delete(context.Background(), items[0]); err != nil {
				t.Fatal(err)
			}
			opts := <-deletes
			if opts.Preconditions == nil || *opts.Preconditions.UID != "original" || *opts.Preconditions.ResourceVersion != "42" {
				t.Fatalf("missing deletion guards: %+v", opts)
			}
		})
	}
}
