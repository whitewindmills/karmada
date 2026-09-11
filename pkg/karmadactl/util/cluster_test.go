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
	"errors"
	"slices"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	kubefake "k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"

	clusterv1alpha1 "github.com/karmada-io/karmada/pkg/apis/cluster/v1alpha1"
	workv1alpha1 "github.com/karmada-io/karmada/pkg/apis/work/v1alpha1"
	karmadafake "github.com/karmada-io/karmada/pkg/generated/clientset/versioned/fake"
	"github.com/karmada-io/karmada/pkg/util"
	"github.com/karmada-io/karmada/pkg/util/names"
)

func TestDeleteClusterObjectForceCleanupErrors(t *testing.T) {
	workError := errors.New("work cleanup failed")
	namespaceError := errors.New("namespace cleanup failed")
	clusterError := errors.New("cluster cleanup failed")
	for _, tt := range []struct {
		name         string
		workErr      error
		namespaceErr error
		clusterErr   error
	}{
		{name: "all cleanup succeeds"},
		{name: "work cleanup fails", workErr: workError},
		{name: "namespace cleanup fails", namespaceErr: namespaceError},
		{name: "cluster cleanup fails", clusterErr: clusterError},
		{name: "all failures are preserved", workErr: workError, namespaceErr: namespaceError, clusterErr: clusterError},
	} {
		t.Run(tt.name, func(t *testing.T) {
			executionSpace := names.GenerateExecutionSpaceName("member")
			cluster := &clusterv1alpha1.Cluster{
				ObjectMeta: metav1.ObjectMeta{Name: "member", Finalizers: []string{util.ClusterControllerFinalizer}},
			}
			work := &workv1alpha1.Work{
				ObjectMeta: metav1.ObjectMeta{Name: "work", Namespace: executionSpace, Finalizers: []string{util.ExecutionControllerFinalizer}},
			}
			karmadaClient := karmadafake.NewClientset(cluster, work)
			kubeClient := kubefake.NewClientset(&corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{Name: executionSpace},
				Spec:       corev1.NamespaceSpec{Finalizers: []corev1.FinalizerName{corev1.FinalizerKubernetes}},
			})
			var attempted []string
			karmadaClient.PrependReactor("delete", "clusters", func(clienttesting.Action) (bool, runtime.Object, error) {
				return true, nil, nil
			})
			karmadaClient.PrependReactor("list", "works", func(clienttesting.Action) (bool, runtime.Object, error) {
				attempted = append(attempted, "work")
				return tt.workErr != nil, nil, tt.workErr
			})
			kubeClient.PrependReactor("*", "namespaces", func(action clienttesting.Action) (bool, runtime.Object, error) {
				if action.GetSubresource() == "finalize" {
					attempted = append(attempted, "namespace")
					return tt.namespaceErr != nil, nil, tt.namespaceErr
				}
				return false, nil, nil
			})
			karmadaClient.PrependReactor("update", "clusters", func(clienttesting.Action) (bool, runtime.Object, error) {
				attempted = append(attempted, "cluster")
				return tt.clusterErr != nil, nil, tt.clusterErr
			})

			err := DeleteClusterObject(kubeClient, karmadaClient, "member", time.Millisecond, false, true)
			wantError := tt.workErr != nil || tt.namespaceErr != nil || tt.clusterErr != nil
			if (err != nil) != wantError {
				t.Errorf("DeleteClusterObject() error = %v, want error %v", err, wantError)
			}
			for _, cause := range []error{tt.workErr, tt.namespaceErr, tt.clusterErr} {
				if cause != nil && !errors.Is(err, cause) {
					t.Errorf("cleanup error %v does not retain cause %v", err, cause)
				}
			}
			if !slices.Equal(attempted, []string{"work", "namespace", "cluster"}) {
				t.Errorf("cleanup attempts = %v, want every cleanup stage in order", attempted)
			}
		})
	}
}

func TestForceCleanupAcceptsConcurrentDeletion(t *testing.T) {
	client := karmadafake.NewClientset(
		&workv1alpha1.Work{ObjectMeta: metav1.ObjectMeta{
			Name: "work", Namespace: "karmada-es-member", Finalizers: []string{util.ExecutionControllerFinalizer},
		}},
		&clusterv1alpha1.Cluster{ObjectMeta: metav1.ObjectMeta{
			Name: "member", Finalizers: []string{util.ClusterControllerFinalizer},
		}},
	)
	client.PrependReactor("update", "*", func(action clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewNotFound(action.GetResource().GroupResource(), "already-deleted")
	})
	if err := removeWorkFinalizer("karmada-es-member", client); err != nil {
		t.Errorf("concurrently deleted Work should be considered cleaned up: %v", err)
	}
	if err := removeClusterFinalizer("member", client); err != nil {
		t.Errorf("concurrently deleted Cluster should be considered cleaned up: %v", err)
	}
	client.PrependReactor("get", "clusters", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewNotFound(schema.GroupResource{Group: clusterv1alpha1.GroupVersion.Group, Resource: "clusters"}, "member")
	})
	if err := removeClusterFinalizer("member", client); err != nil {
		t.Errorf("absent Cluster should be considered cleaned up: %v", err)
	}
}

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
