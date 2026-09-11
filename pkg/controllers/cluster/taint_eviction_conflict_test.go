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

package cluster

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	clusterv1alpha1 "github.com/karmada-io/karmada/pkg/apis/cluster/v1alpha1"
	workv1alpha2 "github.com/karmada-io/karmada/pkg/apis/work/v1alpha2"
	"github.com/karmada-io/karmada/pkg/util"
	"github.com/karmada-io/karmada/pkg/util/fedinformer/keys"
	"github.com/karmada-io/karmada/pkg/util/gclient"
)

type taintEvictionConflictCase struct {
	name        string
	wantUpdates int
	wantErr     bool
}

func TestTaintEvictionRetriesConflictsWithFreshState(t *testing.T) {
	for _, clusterScoped := range []bool{false, true} {
		for _, tt := range []taintEvictionConflictCase{
			{name: "latest replicas and tasks", wantUpdates: 2},
			{name: "cluster recovered", wantUpdates: 1},
			{name: "tolerations updated", wantUpdates: 1},
			{name: "target already removed", wantUpdates: 1},
			{name: "binding deleted", wantUpdates: 1},
			{name: "binding terminating", wantUpdates: 1},
			{name: "non-conflict error", wantUpdates: 1, wantErr: true},
			{name: "persistent conflict", wantUpdates: retry.DefaultRetry.Steps, wantErr: true},
		} {
			t.Run(fmt.Sprintf("cluster-scoped=%t/%s", clusterScoped, tt.name), func(t *testing.T) {
				runTaintEvictionConflictTest(t, clusterScoped, tt)
			})
		}
	}
}

func runTaintEvictionConflictTest(t *testing.T, clusterScoped bool, tt taintEvictionConflictCase) {
	t.Helper()
	cluster := &clusterv1alpha1.Cluster{
		ObjectMeta: metav1.ObjectMeta{Name: "member"},
		Spec: clusterv1alpha1.ClusterSpec{Taints: []corev1.Taint{{
			Key: "unreachable", Effect: corev1.TaintEffectNoExecute,
		}}},
	}
	meta := metav1.ObjectMeta{Name: "binding", Namespace: "default",
		Annotations: map[string]string{util.PolicyPlacementAnnotation: "{}"}}
	spec := workv1alpha2.ResourceBindingSpec{
		Clusters:              []workv1alpha2.TargetCluster{{Name: cluster.Name, Replicas: 2}, {Name: "healthy", Replicas: 1}},
		GracefulEvictionTasks: []workv1alpha2.GracefulEvictionTask{{FromCluster: "completed"}},
	}
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
	updates := 0
	c := fake.NewClientBuilder().WithScheme(gclient.NewSchema()).WithObjects(cluster, binding).
		WithInterceptorFuncs(interceptor.Funcs{
			Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
				updates++
				if tt.name == "non-conflict error" {
					return failure
				}
				if tt.name == "persistent conflict" {
					return conflict
				}
				if updates > 1 {
					return c.Update(ctx, obj, opts...)
				}
				require.NoError(t, c.Get(ctx, key, binding))
				changeTaintEvictionState(ctx, t, c, binding, cluster, tt.name)
				return conflict
			},
		}).Build()
	recorder := record.NewFakeRecorder(20)
	manager := &NoExecuteTaintManager{Client: c, EventRecorder: recorder, EnableNoExecuteTaintEviction: true}
	fedKey := keys.FederatedKey{Cluster: cluster.Name, ClusterWideKey: keys.ClusterWideKey{Name: key.Name, Namespace: key.Namespace}}
	var err error
	if clusterScoped {
		err = manager.syncClusterBindingEviction(fedKey)
	} else {
		err = manager.syncBindingEviction(fedKey)
	}
	if tt.wantErr {
		if tt.name == "persistent conflict" {
			assert.ErrorIs(t, err, conflict)
		} else {
			assert.ErrorIs(t, err, failure)
		}
		assert.Len(t, recorder.Events, 2, "report final write failure once for the binding and its resource")
	} else {
		assert.NoError(t, err)
		assert.Empty(t, recorder.Events, "recovered conflicts must not emit failure events")
	}
	assert.Equal(t, tt.wantUpdates, updates)
	err = c.Get(t.Context(), key, binding)
	if tt.name == "binding deleted" {
		assert.True(t, apierrors.IsNotFound(err))
		return
	}
	require.NoError(t, err)
	current := taintBindingSpecForTest(binding)
	if tt.name == "latest replicas and tasks" {
		assert.Equal(t, []workv1alpha2.TargetCluster{{Name: "healthy", Replicas: 1}}, current.Clusters)
		require.Len(t, current.GracefulEvictionTasks, 1)
		assert.Equal(t, cluster.Name, current.GracefulEvictionTasks[0].FromCluster)
		require.NotNil(t, current.GracefulEvictionTasks[0].Replicas)
		assert.Equal(t, int32(7), *current.GracefulEvictionTasks[0].Replicas)
		assert.Equal(t, "retained", binding.GetLabels()["concurrent-update"])
	} else {
		assert.Equal(t, spec.GracefulEvictionTasks, current.GracefulEvictionTasks)
	}
}

func changeTaintEvictionState(ctx context.Context, t *testing.T, c client.Client, binding client.Object, cluster *clusterv1alpha1.Cluster, scenario string) {
	t.Helper()
	spec := taintBindingSpecForTest(binding)
	switch scenario {
	case "binding deleted", "binding terminating":
		require.NoError(t, c.Delete(ctx, binding))
		return
	case "cluster recovered":
		cluster.Spec.Taints = nil
		require.NoError(t, c.Update(ctx, cluster))
		return
	case "tolerations updated":
		binding.SetAnnotations(map[string]string{util.PolicyPlacementAnnotation: `{"clusterTolerations":[{"operator":"Exists","effect":"NoExecute"}]}`})
	case "target already removed":
		spec.Clusters = spec.Clusters[1:]
	case "latest replicas and tasks":
		spec.Clusters[0].Replicas = 7
		spec.GracefulEvictionTasks = nil
		binding.SetLabels(map[string]string{"concurrent-update": "retained"})
	}
	require.NoError(t, c.Update(ctx, binding))
}

func taintBindingSpecForTest(binding client.Object) *workv1alpha2.ResourceBindingSpec {
	if rb, ok := binding.(*workv1alpha2.ResourceBinding); ok {
		return &rb.Spec
	}
	return &binding.(*workv1alpha2.ClusterResourceBinding).Spec
}
