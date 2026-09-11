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
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/karmada-io/karmada/pkg/util"
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

func TestForgetVersionRecordDoesNotTouchMemberResources(t *testing.T) {
	object := &unstructured.Unstructured{}
	object.SetGroupVersionKind(corev1.SchemeGroupVersion.WithKind("ConfigMap"))
	object.SetNamespace("default")
	object.SetName("preserved")
	object.SetResourceVersion("1")
	another := object.DeepCopy()
	another.SetName("another")
	watcher := &objectWatcherImpl{VersionRecord: make(map[string]map[string]string)}
	watcher.recordVersion(object, "member")
	watcher.recordVersion(another, "member")
	watcher.recordVersion(object, "other-member")

	for range 2 {
		watcher.ForgetVersionRecord("member", object)
		_, exists := watcher.GetVersionRecord("member", object)
		assert.False(t, exists)
	}
	version, exists := watcher.GetVersionRecord("member", another)
	assert.True(t, exists)
	assert.Equal(t, "1", version)
	version, exists = watcher.GetVersionRecord("other-member", object)
	assert.True(t, exists)
	assert.Equal(t, "1", version)
	watcher.ForgetVersionRecord("member", another)
	assert.NotContains(t, watcher.VersionRecord, "member")
	assert.Contains(t, watcher.VersionRecord, "other-member")
}

func TestDeleteUsesObservedUID(t *testing.T) {
	for _, tt := range []struct {
		name        string
		recreated   bool
		alreadyGone bool
	}{
		{name: "delete the observed resource"},
		{name: "preserve a resource recreated after observation", recreated: true},
		{name: "already deleted resource still succeeds", alreadyGone: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			observed := &unstructured.Unstructured{}
			observed.SetGroupVersionKind(corev1.SchemeGroupVersion.WithKind("ConfigMap"))
			observed.SetNamespace("default")
			observed.SetName("test")
			observed.SetUID("observed-uid")
			observed.SetResourceVersion("1")
			observed.SetLabels(map[string]string{util.ManagedByKarmadaLabel: util.ManagedByKarmadaLabelValue})
			desired := observed.DeepCopy()
			desired.SetUID("")
			memberObject := observed.DeepCopy()
			if tt.recreated {
				memberObject.SetUID("replacement-uid")
				memberObject.SetLabels(nil)
			}

			scheme := runtime.NewScheme()
			require.NoError(t, corev1.AddToScheme(scheme))
			var objects []runtime.Object
			if !tt.alreadyGone {
				objects = append(objects, memberObject)
			}
			dynamicClient := fake.NewSimpleDynamicClient(scheme, objects...)
			gvr := corev1.SchemeGroupVersion.WithResource("configmaps")
			var deleteOptions metav1.DeleteOptions
			// The fake tracker does not enforce UID preconditions, so model the API server's check.
			dynamicClient.PrependReactor("delete", "configmaps", func(action clienttesting.Action) (bool, runtime.Object, error) {
				deleteAction := action.(clienttesting.DeleteAction)
				deleteOptions = deleteAction.GetDeleteOptions()
				if preconditions := deleteOptions.Preconditions; preconditions != nil && preconditions.UID != nil && *preconditions.UID != memberObject.GetUID() {
					return true, nil, apierrors.NewConflict(gvr.GroupResource(), deleteAction.GetName(), errors.New("UID precondition failed"))
				}
				return false, nil, nil
			})

			mapper := meta.NewDefaultRESTMapper([]schema.GroupVersion{corev1.SchemeGroupVersion})
			mapper.Add(observed.GroupVersionKind(), meta.RESTScopeNamespace)
			indexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})
			require.NoError(t, indexer.Add(observed))
			manager := informertesting.NewFakeSingleClusterManager(true, true, func(gvr schema.GroupVersionResource) cache.GenericLister {
				return cache.NewGenericLister(indexer, gvr.GroupResource())
			})
			watcher := &objectWatcherImpl{
				RESTMapper: mapper,
				InformerManager: informertesting.NewFakeMultiClusterInformerManager(map[string]genericmanager.SingleClusterInformerManager{
					"member": manager,
				}),
				ClusterClientSetFunc: func(clusterName string, _ client.Client, _ *util.ClientOption) (*util.DynamicClusterClient, error) {
					return &util.DynamicClusterClient{ClusterName: clusterName, DynamicClientSet: dynamicClient}, nil
				},
				VersionRecord: make(map[string]map[string]string),
			}
			watcher.recordVersion(observed, "member")

			err := watcher.Delete(t.Context(), "member", desired)
			if tt.recreated {
				assert.True(t, apierrors.IsConflict(err), "expected a UID conflict, got %v", err)
				remaining, getErr := dynamicClient.Resource(gvr).Namespace("default").Get(t.Context(), "test", metav1.GetOptions{})
				require.NoError(t, getErr)
				assert.Equal(t, memberObject.GetUID(), remaining.GetUID())
			} else {
				require.NoError(t, err)
				_, getErr := dynamicClient.Resource(gvr).Namespace("default").Get(t.Context(), "test", metav1.GetOptions{})
				assert.True(t, apierrors.IsNotFound(getErr))
			}
			_, recorded := watcher.GetVersionRecord("member", observed)
			assert.Equal(t, tt.recreated, recorded, "retain the version record only when deletion needs retrying")
			require.NotNil(t, deleteOptions.Preconditions)
			require.NotNil(t, deleteOptions.Preconditions.UID)
			assert.Equal(t, observed.GetUID(), *deleteOptions.Preconditions.UID)
			require.NotNil(t, deleteOptions.PropagationPolicy)
			assert.Equal(t, metav1.DeletePropagationBackground, *deleteOptions.PropagationPolicy)
		})
	}
}
