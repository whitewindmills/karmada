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

package provider

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes/fake"
	listv1 "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"

	clusterv1alpha1 "github.com/karmada-io/karmada/pkg/apis/cluster/v1alpha1"
	clusterlister "github.com/karmada-io/karmada/pkg/generated/listers/cluster/v1alpha1"
	"github.com/karmada-io/karmada/pkg/util/fedinformer/typedmanager"
)

func TestNodeListerRequestMetadataIsolation(t *testing.T) {
	tests := []struct {
		name        string
		annotations map[string]string
	}{
		{
			name: "without cached annotations",
		},
		{
			name: "with cached annotations",
			annotations: map[string]string{
				"example.com/note":              "preserve",
				labelSelectorAnnotationInternal: "user-supplied",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{
				Name:        "worker",
				Labels:      map[string]string{"pool": "workers", "zone": "east"},
				Annotations: tt.annotations,
			}}
			clusterIndexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})
			require.NoError(t, clusterIndexer.Add(&clusterv1alpha1.Cluster{
				ObjectMeta: metav1.ObjectMeta{Name: "member1"},
			}))
			manager := typedmanager.NewMultiClusterInformerManager(t.Context(), nil)
			sci := manager.ForCluster("member1", fake.NewClientset(node), 0)
			rawLister, err := sci.Lister(NodesGVR)
			require.NoError(t, err)
			sci.Start()
			t.Cleanup(sci.Stop)
			require.True(t, sci.WaitForCacheSyncWithTimeout(5 * time.Second)[NodesGVR])
			cachedNode, err := rawLister.(listv1.NodeLister).Get(node.Name)
			require.NoError(t, err)
			originalAnnotations := cachedNode.DeepCopy().Annotations
			lister := NewNodeLister(clusterlister.NewClusterLister(clusterIndexer), manager)

			firstSelector := labels.SelectorFromSet(labels.Set{"pool": "workers"})
			first, err := lister.List(firstSelector)
			require.NoError(t, err)
			require.Len(t, first, 1)
			assert.Equal(t, firstSelector.String(), first[0].Annotations[labelSelectorAnnotationInternal])
			assert.Equal(t, originalAnnotations, cachedNode.Annotations, "list must not modify the informer cache")
			assert.Equal(t, tt.annotations["example.com/note"], first[0].Annotations["example.com/note"])

			secondSelector := labels.SelectorFromSet(labels.Set{"zone": "east"})
			second, err := lister.List(secondSelector)
			require.NoError(t, err)
			require.Len(t, second, 1)
			assert.Equal(t, firstSelector.String(), first[0].Annotations[labelSelectorAnnotationInternal],
				"another list must not replace the first request's selector")
			assert.Equal(t, secondSelector.String(), second[0].Annotations[labelSelectorAnnotationInternal])

			byName, err := lister.Get(node.Name)
			require.NoError(t, err)
			assert.NotContains(t, byName.Annotations, labelSelectorAnnotationInternal)
			assert.Equal(t, tt.annotations["example.com/note"], byName.Annotations["example.com/note"])
			assert.Equal(t, firstSelector.String(), first[0].Annotations[labelSelectorAnnotationInternal],
				"get must not turn a pending list into a name query")
			assert.Equal(t, secondSelector.String(), second[0].Annotations[labelSelectorAnnotationInternal])

			_, err = lister.List(firstSelector)
			require.NoError(t, err)
			assert.NotContains(t, byName.Annotations, labelSelectorAnnotationInternal,
				"list must not turn a pending name query into a selector query")
			assert.Equal(t, originalAnnotations, cachedNode.Annotations, "requests must leave the informer cache unchanged")
		})
	}
}
