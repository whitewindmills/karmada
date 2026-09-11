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

package util

import (
	"context"
	"slices"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	kubefake "k8s.io/client-go/kubernetes/fake"
)

func TestRemoveExecutionSpaceFinalizer(t *testing.T) {
	for _, tt := range []struct {
		name       string
		finalizers []corev1.FinalizerName
		want       []corev1.FinalizerName
		missing    bool
		finalized  bool
	}{
		{name: "remove namespace controller finalizer", finalizers: []corev1.FinalizerName{corev1.FinalizerKubernetes}, finalized: true},
		{name: "preserve other spec finalizers", finalizers: []corev1.FinalizerName{"example.com/cleanup", corev1.FinalizerKubernetes}, want: []corev1.FinalizerName{"example.com/cleanup"}, finalized: true},
		{name: "already finalized"},
		{name: "unrelated finalizer", finalizers: []corev1.FinalizerName{"example.com/cleanup"}, want: []corev1.FinalizerName{"example.com/cleanup"}},
		{name: "namespace already removed", missing: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			namespace := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{Name: "karmada-es-member", Finalizers: []string{"example.com/metadata"}},
				Spec:       corev1.NamespaceSpec{Finalizers: tt.finalizers},
			}
			var objects []runtime.Object
			if !tt.missing {
				objects = append(objects, namespace)
			}
			client := kubefake.NewClientset(objects...)
			if err := removeExecutionSpaceFinalizer(namespace.Name, client); err != nil {
				t.Fatal(err)
			}
			finalized := false
			for _, action := range client.Actions() {
				if action.GetSubresource() == "finalize" {
					finalized = true
				}
				if action.GetVerb() == "update" && action.GetSubresource() == "" {
					t.Error("namespace finalizers must not be changed through a normal update")
				}
			}
			if finalized != tt.finalized {
				t.Errorf("finalize called = %v, want %v", finalized, tt.finalized)
			}
			got, err := client.CoreV1().Namespaces().Get(context.Background(), namespace.Name, metav1.GetOptions{})
			if tt.missing {
				if !apierrors.IsNotFound(err) {
					t.Errorf("expected namespace to remain absent, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got.Spec.Finalizers, tt.want) {
				t.Errorf("spec.finalizers = %v, want %v", got.Spec.Finalizers, tt.want)
			}
			if !slices.Equal(got.Finalizers, namespace.Finalizers) {
				t.Errorf("unrelated metadata finalizers were changed: %v", got.Finalizers)
			}
		})
	}
}
