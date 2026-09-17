package ufei

import (
	"context"
	"fmt"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
)

var eipResource = schema.GroupVersionResource{Group: "k8s.ovn.org", Version: "v1", Resource: "egressips"}

type EgressIP struct {
	metav1.ObjectMeta `json:"metadata"`
	Spec              struct {
		EgressIPs         []string             `json:"egressIPs"`
		NamespaceSelector metav1.LabelSelector `json:"namespaceSelector"`
		PodSelector       metav1.LabelSelector `json:"podSelector"`
	} `json:"spec"`
	Status struct {
		Items []struct {
			EgressIP string `json:"egressIP"`
			Node     string `json:"node"`
		} `json:"items"`
	} `json:"status"`
}

type EgressAPI interface {
	Snapshot(context.Context) ([]EgressIP, error)
	Delete(context.Context, EgressIP) error
}

type Kube struct {
	client dynamic.Interface
	config Config
}

func NewKube(c Config) (*Kube, error) {
	cfg, err := rest.InClusterConfig()
	if err != nil {
		return nil, err
	}
	cfg.Timeout = c.APITimeout
	client, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	return &Kube{client, c}, nil
}

func (k *Kube) Snapshot(ctx context.Context) ([]EgressIP, error) {
	ns, err := k.client.Resource(schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}).Get(ctx, k.config.Namespace, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("read namespace: %w", err)
	}
	pod, err := k.client.Resource(schema.GroupVersionResource{Version: "v1", Resource: "pods"}).Namespace(k.config.Namespace).Get(ctx, k.config.PodName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("read probe pod: %w", err)
	}
	var result []EgressIP
	for _, name := range k.config.EgressIPs {
		list, err := k.client.Resource(eipResource).List(ctx, metav1.ListOptions{FieldSelector: "metadata.name=" + name})
		if err != nil {
			return nil, fmt.Errorf("list EgressIP %s: %w", name, err)
		}
		if len(list.Items) != 1 || list.Items[0].GetName() != name {
			return nil, fmt.Errorf("EgressIP %s is missing", name)
		}
		var e EgressIP
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(list.Items[0].Object, &e); err != nil {
			return nil, err
		}
		if e.UID == "" || e.ResourceVersion == "" || e.DeletionTimestamp != nil {
			return nil, fmt.Errorf("EgressIP %s is not stable", name)
		}
		for _, s := range []struct {
			selector metav1.LabelSelector
			labels   map[string]string
		}{{e.Spec.NamespaceSelector, ns.GetLabels()}, {e.Spec.PodSelector, pod.GetLabels()}} {
			selector, err := metav1.LabelSelectorAsSelector(&s.selector)
			if err != nil {
				return nil, err
			}
			if !selector.Matches(labels.Set(s.labels)) {
				return nil, fmt.Errorf("EgressIP %s does not select this probe pod", name)
			}
		}
		if len(e.Spec.EgressIPs) == 0 {
			return nil, fmt.Errorf("EgressIP %s has no IPs", name)
		}
		for _, ip := range e.Spec.EgressIPs {
			assigned := false
			for _, item := range e.Status.Items {
				if item.EgressIP == ip && item.Node != "" {
					assigned = true
				}
			}
			if !assigned {
				return nil, fmt.Errorf("EgressIP %s address %s is unassigned", name, ip)
			}
		}
		result = append(result, e)
	}
	return result, nil
}

func (k *Kube) Delete(ctx context.Context, e EgressIP) error {
	// Both guards matter: do not delete a recreated object or changed selectors/spec.
	return k.client.Resource(eipResource).Delete(ctx, e.Name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &e.UID, ResourceVersion: &e.ResourceVersion}})
}

func fingerprint(items []EgressIP) string {
	var b strings.Builder
	for _, e := range items {
		fmt.Fprintf(&b, "%s/%s/%s;", e.Name, e.UID, e.ResourceVersion)
	}
	return b.String()
}
