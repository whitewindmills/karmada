/*
Copyright 2026 The Karmada Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package native

import (
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/karmada-io/karmada/pkg/resourceinterpreter/default/native/prune"
	"github.com/karmada-io/karmada/pkg/util/helper"
)

func TestPruneServiceAllocatedPorts(t *testing.T) {
	for _, serviceType := range []corev1.ServiceType{corev1.ServiceTypeNodePort, corev1.ServiceTypeLoadBalancer} {
		t.Run(string(serviceType), func(t *testing.T) {
			service := &corev1.Service{
				TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Service"},
				Spec: corev1.ServiceSpec{
					Type:       serviceType,
					ClusterIP:  "10.0.0.1",
					ClusterIPs: []string{"10.0.0.1"},
					Ports: []corev1.ServicePort{
						{Name: "http", Protocol: corev1.ProtocolTCP, Port: 80, TargetPort: intstr.FromInt32(8080), NodePort: 30675, AppProtocol: new("http")},
						{Name: "dns", Protocol: corev1.ProtocolUDP, Port: 53, TargetPort: intstr.FromInt32(5353), NodePort: 30676},
					},
				},
			}
			if serviceType == corev1.ServiceTypeLoadBalancer {
				service.Spec.ExternalTrafficPolicy = corev1.ServiceExternalTrafficPolicyLocal
				service.Spec.HealthCheckNodePort = 30677
			}
			workload, err := helper.ToUnstructured(service)
			if err != nil {
				t.Fatal(err)
			}
			if err := prune.RemoveIrrelevantFields(workload); err != nil {
				t.Fatal(err)
			}
			got := &corev1.Service{}
			if err := helper.ConvertToTypedObject(workload, got); err != nil {
				t.Fatal(err)
			}
			want := service.Spec.DeepCopy()
			want.ClusterIP, want.ClusterIPs, want.HealthCheckNodePort = "", nil, 0
			for i := range want.Ports {
				want.Ports[i].NodePort = 0
			}
			if !reflect.DeepEqual(got.Spec, *want) {
				t.Errorf("pruned Service spec = %+v, want %+v", got.Spec, *want)
			}
		})
	}
}

func TestRetainServiceNodePorts(t *testing.T) {
	for _, tt := range []struct {
		name        string
		serviceType corev1.ServiceType
		ports       []corev1.ServicePort
		want        []int32
	}{
		{
			name: "reordered ports retain their own allocations", serviceType: corev1.ServiceTypeNodePort,
			ports: []corev1.ServicePort{{Name: "dns", Protocol: corev1.ProtocolUDP, Port: 53}, {Name: "http", Protocol: corev1.ProtocolTCP, Port: 80}},
			want:  []int32{31676, 31675},
		},
		{
			name: "load balancer keeps member ports", serviceType: corev1.ServiceTypeLoadBalancer,
			ports: []corev1.ServicePort{{Name: "http", Protocol: corev1.ProtocolTCP, Port: 80}},
			want:  []int32{31675},
		},
		{
			name: "explicit override wins", serviceType: corev1.ServiceTypeNodePort,
			ports: []corev1.ServicePort{{Name: "http", Protocol: corev1.ProtocolTCP, Port: 80, NodePort: 32080}},
			want:  []int32{32080},
		},
		{
			name: "new port is allocated by member", serviceType: corev1.ServiceTypeNodePort,
			ports: []corev1.ServicePort{{Name: "new", Protocol: corev1.ProtocolTCP, Port: 81}},
			want:  []int32{0},
		},
		{
			name: "protocol change is not the same port", serviceType: corev1.ServiceTypeNodePort,
			ports: []corev1.ServicePort{{Name: "http", Protocol: corev1.ProtocolUDP, Port: 80}},
			want:  []int32{0},
		},
		{
			name: "changing to ClusterIP drops node ports", serviceType: corev1.ServiceTypeClusterIP,
			ports: []corev1.ServicePort{{Name: "http", Protocol: corev1.ProtocolTCP, Port: 80}},
			want:  []int32{0},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			desired, err := helper.ToUnstructured(&corev1.Service{
				TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Service"},
				Spec:     corev1.ServiceSpec{Type: tt.serviceType, Ports: tt.ports},
			})
			if err != nil {
				t.Fatal(err)
			}
			observed, err := helper.ToUnstructured(&corev1.Service{
				TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Service"},
				Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeNodePort, ClusterIP: "10.1.0.1", Ports: []corev1.ServicePort{
					{Name: "http", Protocol: corev1.ProtocolTCP, Port: 80, NodePort: 31675},
					{Name: "dns", Protocol: corev1.ProtocolUDP, Port: 53, NodePort: 31676},
				}},
			})
			if err != nil {
				t.Fatal(err)
			}
			original := observed.DeepCopy()
			retained, err := NewDefaultInterpreter().Retain(desired, observed)
			if err != nil {
				t.Fatal(err)
			}
			got := &corev1.Service{}
			if err := helper.ConvertToTypedObject(retained, got); err != nil {
				t.Fatal(err)
			}
			for i, port := range got.Spec.Ports {
				if port.NodePort != tt.want[i] {
					t.Errorf("port %s nodePort = %d, want %d", port.Name, port.NodePort, tt.want[i])
				}
			}
			if got.Spec.ClusterIP != "10.1.0.1" || !reflect.DeepEqual(observed, original) {
				t.Error("Service retention lost the cluster IP or mutated the observed resource")
			}
		})
	}
}

func TestPruneServiceInvalidPorts(t *testing.T) {
	for _, ports := range []any{"invalid", []any{"invalid"}} {
		workload := &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "v1", "kind": "Service", "spec": map[string]any{"ports": ports},
		}}
		if err := prune.RemoveIrrelevantFields(workload); err == nil {
			t.Errorf("invalid service ports %v were silently accepted", ports)
		}
	}
}
