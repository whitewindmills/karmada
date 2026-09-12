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

package framework

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

func TestResourceQuotaStatusReady(t *testing.T) {
	hard := corev1.ResourceList{
		corev1.ResourceRequestsCPU:    resource.MustParse("30m"),
		corev1.ResourceRequestsMemory: resource.MustParse("1Gi"),
	}
	used := corev1.ResourceList{
		corev1.ResourceRequestsCPU:    resource.MustParse("0"),
		corev1.ResourceRequestsMemory: resource.MustParse("0"),
	}
	for _, tt := range []struct {
		name   string
		status corev1.ResourceQuotaStatus
		ready  bool
	}{
		{name: "created without accounting"},
		{name: "hard limits without usage", status: corev1.ResourceQuotaStatus{Hard: hard}},
		{name: "usage without hard limits", status: corev1.ResourceQuotaStatus{Used: used}},
		{name: "incomplete usage", status: corev1.ResourceQuotaStatus{
			Hard: hard, Used: corev1.ResourceList{corev1.ResourceRequestsCPU: resource.MustParse("0")},
		}},
		{name: "old hard limits", status: corev1.ResourceQuotaStatus{
			Hard: corev1.ResourceList{
				corev1.ResourceRequestsCPU: resource.MustParse("20m"), corev1.ResourceRequestsMemory: resource.MustParse("1Gi"),
			},
			Used: used,
		}},
		{name: "known zero usage", status: corev1.ResourceQuotaStatus{Hard: hard, Used: used}, ready: true},
		{name: "equivalent quantity formats", status: corev1.ResourceQuotaStatus{
			Hard: corev1.ResourceList{
				corev1.ResourceRequestsCPU: resource.MustParse("0.03"), corev1.ResourceRequestsMemory: resource.MustParse("1024Mi"),
			},
			Used: used,
		}, ready: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			quota := &corev1.ResourceQuota{Spec: corev1.ResourceQuotaSpec{Hard: hard}, Status: tt.status}
			if ready := resourceQuotaStatusReady(quota); ready != tt.ready {
				t.Errorf("quota status ready = %v, want %v; status=%v", ready, tt.ready, tt.status)
			}
		})
	}
}
