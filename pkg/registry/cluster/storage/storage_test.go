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

package storage

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apiserver/pkg/registry/rest"

	clusterapis "github.com/karmada-io/karmada/pkg/apis/cluster"
	workv1alpha2 "github.com/karmada-io/karmada/pkg/apis/work/v1alpha2"
)

func TestClusterDeletionProtectionValidation(t *testing.T) {
	var nilCluster *clusterapis.Cluster
	for _, tt := range []struct {
		name      string
		object    runtime.Object
		wantError bool
		forbidden bool
	}{
		{name: "unprotected", object: &clusterapis.Cluster{ObjectMeta: metav1.ObjectMeta{Name: "member"}}},
		{name: "non-protecting label", object: &clusterapis.Cluster{ObjectMeta: metav1.ObjectMeta{Name: "member", Labels: map[string]string{workv1alpha2.DeletionProtectionLabelKey: "Never"}}}},
		{name: "protected", object: &clusterapis.Cluster{ObjectMeta: metav1.ObjectMeta{Name: "member", Labels: map[string]string{workv1alpha2.DeletionProtectionLabelKey: workv1alpha2.DeletionProtectionAlways}}}, wantError: true, forbidden: true},
		{name: "wrong type", object: &corev1.ConfigMap{}, wantError: true},
		{name: "nil object", wantError: true},
		{name: "typed nil cluster", object: nilCluster, wantError: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := withDeletionProtection(nil)(t.Context(), tt.object)
			if (err != nil) != tt.wantError || apierrors.IsForbidden(err) != tt.forbidden {
				t.Errorf("validation error = %v, want error=%v forbidden=%v", err, tt.wantError, tt.forbidden)
			}
		})
	}
}

func TestClusterDeletionProtectionPreservesCallerValidation(t *testing.T) {
	callerError := errors.New("caller validation failed")
	for _, protected := range []bool{false, true} {
		cluster := &clusterapis.Cluster{ObjectMeta: metav1.ObjectMeta{Name: "member"}}
		if protected {
			cluster.Labels = map[string]string{workv1alpha2.DeletionProtectionLabelKey: workv1alpha2.DeletionProtectionAlways}
		}
		called := false
		validation := rest.ValidateObjectFunc(func(_ context.Context, obj runtime.Object) error {
			called = true
			if obj != cluster {
				t.Error("caller validator received a different object")
			}
			return callerError
		})
		err := withDeletionProtection(validation)(t.Context(), cluster)
		if protected {
			if !apierrors.IsForbidden(err) || called {
				t.Errorf("protected object reached caller validation: error=%v called=%v", err, called)
			}
		} else if !errors.Is(err, callerError) || !called {
			t.Errorf("caller validation was not preserved: error=%v called=%v", err, called)
		}
	}
}
