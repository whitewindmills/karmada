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

package objectwatcher

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/cache"

	"github.com/karmada-io/karmada/pkg/util/fedinformer/genericmanager"
	informertesting "github.com/karmada-io/karmada/pkg/util/fedinformer/genericmanager/testing"
)

func TestDeleteCleansUpVersionRecord(t *testing.T) {
	tests := []struct {
		name                string
		unmanagedObject     bool
		missingInformer     bool
		anotherResource     bool
		wantErr             bool
		wantVersionRecorded bool
	}{
		{name: "object already deleted"},
		{name: "object no longer managed", unmanagedObject: true},
		{name: "another resource in the cluster is preserved", anotherResource: true},
		{name: "lookup failure preserves version for retry", missingInformer: true, wantErr: true, wantVersionRecorded: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resource := &unstructured.Unstructured{}
			resource.SetGroupVersionKind(corev1.SchemeGroupVersion.WithKind("ConfigMap"))
			resource.SetNamespace("default")
			resource.SetName("test")
			resource.SetResourceVersion("1")
			another := resource.DeepCopy()
			another.SetName("another")

			mapper := meta.NewDefaultRESTMapper([]schema.GroupVersion{corev1.SchemeGroupVersion})
			mapper.Add(resource.GroupVersionKind(), meta.RESTScopeNamespace)
			indexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})
			if tt.unmanagedObject {
				require.NoError(t, indexer.Add(resource.DeepCopy()))
			}
			managers := make(map[string]genericmanager.SingleClusterInformerManager)
			if !tt.missingInformer {
				managers["member"] = informertesting.NewFakeSingleClusterManager(true, true, func(gvr schema.GroupVersionResource) cache.GenericLister {
					return cache.NewGenericLister(indexer, gvr.GroupResource())
				})
			}
			watcher := &objectWatcherImpl{
				RESTMapper:      mapper,
				InformerManager: informertesting.NewFakeMultiClusterInformerManager(managers),
				VersionRecord:   make(map[string]map[string]string),
			}
			watcher.recordVersion(resource, "member")
			watcher.recordVersion(resource, "another-member")
			if tt.anotherResource {
				watcher.recordVersion(another, "member")
			}

			err := watcher.Delete(t.Context(), "member", resource)
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			_, recorded := watcher.GetVersionRecord("member", resource)
			assert.Equal(t, tt.wantVersionRecorded, recorded)
			_, clusterRecorded := watcher.VersionRecord["member"]
			assert.Equal(t, tt.wantVersionRecorded || tt.anotherResource, clusterRecorded, "empty cluster records should be removed")
			version, recorded := watcher.GetVersionRecord("another-member", resource)
			assert.True(t, recorded)
			assert.Equal(t, "1", version)
			if tt.anotherResource {
				version, recorded = watcher.GetVersionRecord("member", another)
				assert.True(t, recorded)
				assert.Equal(t, "1", version)
			}
		})
	}
}
