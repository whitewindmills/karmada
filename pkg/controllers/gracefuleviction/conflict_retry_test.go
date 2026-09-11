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

package gracefuleviction

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	workv1alpha2 "github.com/karmada-io/karmada/pkg/apis/work/v1alpha2"
)

type evictionConflictTestCase struct {
	name        string
	wantPatches int
	wantEvents  int
	wantErr     bool
}

func TestGracefulEvictionRetriesConflictsWithFreshState(t *testing.T) {
	for _, clusterScoped := range []bool{false, true} {
		for _, tt := range []evictionConflictTestCase{
			{name: "concurrent task appended", wantPatches: 2, wantEvents: 2},
			{name: "task removed by another controller", wantPatches: 1},
			{name: "binding deleted", wantPatches: 1},
			{name: "binding terminating", wantPatches: 1},
			{name: "non-conflict error", wantPatches: 1, wantErr: true},
			{name: "persistent conflict", wantPatches: retry.DefaultRetry.Steps, wantErr: true},
		} {
			scope := "ResourceBinding"
			if clusterScoped {
				scope = "ClusterResourceBinding"
			}
			t.Run(scope+"/"+tt.name, func(t *testing.T) {
				runGracefulEvictionConflictTest(t, clusterScoped, tt)
			})
		}
	}
}

func runGracefulEvictionConflictTest(t *testing.T, clusterScoped bool, tt evictionConflictTestCase) {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, workv1alpha2.Install(scheme))
	meta := metav1.ObjectMeta{Name: "binding", Namespace: "default", Generation: 1}
	spec := workv1alpha2.ResourceBindingSpec{GracefulEvictionTasks: []workv1alpha2.GracefulEvictionTask{{
		FromCluster: "old", CreationTimestamp: new(metav1.NewTime(time.Now().Add(-time.Hour))),
	}}}
	var binding client.Object = &workv1alpha2.ResourceBinding{ObjectMeta: meta, Spec: spec}
	resource := schema.GroupResource{Group: workv1alpha2.GroupVersion.Group, Resource: "resourcebindings"}
	if clusterScoped {
		meta.Namespace = ""
		binding = &workv1alpha2.ClusterResourceBinding{ObjectMeta: meta, Spec: spec}
		resource.Resource = "clusterresourcebindings"
	}
	if tt.name == "binding terminating" {
		binding.SetFinalizers([]string{"example.com/cleanup"})
	}
	key := client.ObjectKeyFromObject(binding)
	conflict := apierrors.NewConflict(resource, binding.GetName(), errors.New("concurrent write"))
	failure := errors.New("API unavailable")
	patches := 0
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(binding).
		WithInterceptorFuncs(interceptor.Funcs{
			Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
				patches++
				if tt.name == "non-conflict error" {
					return failure
				}
				if tt.name == "persistent conflict" {
					return conflict
				}
				if patches > 1 {
					return c.Patch(ctx, obj, patch, opts...)
				}
				require.NoError(t, c.Get(ctx, key, binding))
				if tt.name == "binding deleted" || tt.name == "binding terminating" {
					require.NoError(t, c.Delete(ctx, binding))
					return conflict
				}
				current := bindingSpecForConflictTest(binding)
				if tt.name == "task removed by another controller" {
					current.GracefulEvictionTasks = nil
				} else {
					current.GracefulEvictionTasks = append(current.GracefulEvictionTasks,
						workv1alpha2.GracefulEvictionTask{FromCluster: "concurrent", SuppressDeletion: new(true)})
				}
				binding.SetLabels(map[string]string{"concurrent-update": "retained"})
				require.NoError(t, c.Update(ctx, binding))
				return conflict
			},
		}).Build()
	recorder := record.NewFakeRecorder(20)
	var controller reconcile.Reconciler = &RBGracefulEvictionController{
		Client: c, EventRecorder: recorder, GracefulEvictionTimeout: time.Minute,
	}
	if clusterScoped {
		controller = &CRBGracefulEvictionController{
			Client: c, EventRecorder: recorder, GracefulEvictionTimeout: time.Minute,
		}
	}
	_, err := controller.Reconcile(t.Context(), reconcile.Request{NamespacedName: key})
	if tt.wantErr {
		if tt.name == "persistent conflict" {
			assert.ErrorIs(t, err, conflict)
		} else {
			assert.ErrorIs(t, err, failure)
		}
	} else {
		assert.NoError(t, err)
	}
	assert.Equal(t, tt.wantPatches, patches)
	assert.Len(t, recorder.Events, tt.wantEvents, "only committed removals should emit success events")
	err = c.Get(t.Context(), key, binding)
	if tt.name == "binding deleted" {
		assert.True(t, apierrors.IsNotFound(err))
		return
	}
	require.NoError(t, err)
	if tt.name == "concurrent task appended" {
		tasks := bindingSpecForConflictTest(binding).GracefulEvictionTasks
		require.Len(t, tasks, 1)
		assert.Equal(t, "concurrent", tasks[0].FromCluster)
		assert.False(t, tasks[0].CreationTimestamp.IsZero())
		assert.Equal(t, "retained", binding.GetLabels()["concurrent-update"])
	}
}

func bindingSpecForConflictTest(binding client.Object) *workv1alpha2.ResourceBindingSpec {
	if rb, ok := binding.(*workv1alpha2.ResourceBinding); ok {
		return &rb.Spec
	}
	return &binding.(*workv1alpha2.ClusterResourceBinding).Spec
}
