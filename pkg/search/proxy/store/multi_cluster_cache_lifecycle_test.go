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

package store

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"testing/synctest"

	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	genericregistry "k8s.io/apiserver/pkg/registry/generic/registry"
	"k8s.io/apiserver/pkg/storage"
)

type resourceVersionStorage struct {
	storage.Interface
	fetch func(context.Context) (string, error)
}

func (s *resourceVersionStorage) GetList(ctx context.Context, _ string, _ storage.ListOptions, obj runtime.Object) error {
	rv, err := s.fetch(ctx)
	if err != nil {
		return err
	}
	obj.(*unstructured.UnstructuredList).SetResourceVersion(rv)
	return nil
}

func resourceVersionClusterCache(fetch func(context.Context) (string, error)) *clusterCache {
	return &clusterCache{
		cache: map[schema.GroupVersionResource]*resourceCache{
			podGVR: {
				Store: &genericregistry.Store{
					DefaultQualifiedResource: podGVR.GroupResource(),
					NewListFunc:              func() runtime.Object { return &unstructured.UnstructuredList{} },
					KeyRootFunc:              func(context.Context) string { return "/pods" },
					PredicateFunc: func(label labels.Selector, field fields.Selector) storage.SelectionPredicate {
						return storage.SelectionPredicate{Label: label, Field: field}
					},
					Storage: genericregistry.DryRunnableStorage{
						Storage: &resourceVersionStorage{fetch: fetch},
					},
				},
			},
		},
	}
}

func TestMultiClusterCache_VersionLookupErrorsDoNotLeak(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		listErr := errors.New("member list failed")
		cache := NewMultiClusterCache(nil, nil)
		clusters := []string{"cluster1", "cluster2"}
		for _, cluster := range clusters {
			cache.cache[cluster] = resourceVersionClusterCache(func(context.Context) (string, error) {
				return "", listErr
			})
		}

		versions := newMultiClusterResourceVersionFromString("")
		assert.ErrorIs(t, cache.fillMissingClusterResourceVersion(t.Context(), versions, clusters, podGVR), listErr)
	})
}

func TestMultiClusterCache_VersionLookupFailureCancelsPendingRequests(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		listErr := errors.New("member list failed")
		pendingStarted := make(chan struct{})
		pendingFinished := make(chan struct{})
		cache := NewMultiClusterCache(nil, nil)
		cache.cache["failed"] = resourceVersionClusterCache(func(context.Context) (string, error) {
			<-pendingStarted
			return "", listErr
		})
		cache.cache["pending"] = resourceVersionClusterCache(func(ctx context.Context) (string, error) {
			close(pendingStarted)
			<-ctx.Done()
			close(pendingFinished)
			return "", ctx.Err()
		})

		versions := newMultiClusterResourceVersionFromString("")
		assert.ErrorIs(t, cache.fillMissingClusterResourceVersion(t.Context(), versions, []string{"failed", "pending"}, podGVR), listErr)
		select {
		case <-pendingFinished:
		default:
			t.Error("version lookup returned before cancelling and joining the pending member request")
		}
	})
}

func TestMultiClusterCache_VersionLookupConcurrentSuccess(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cache := NewMultiClusterCache(nil, nil)
		versions := newMultiClusterResourceVersionFromString("")
		versions.set("existing", "42")
		want := map[string]string{"existing": "42"}
		clusters := []string{"existing", "unregistered"}
		cache.cache["existing"] = resourceVersionClusterCache(func(context.Context) (string, error) {
			return "", errors.New("an existing resource version must not be fetched again")
		})
		for i := range 32 {
			cluster, rv := fmt.Sprintf("cluster%d", i), fmt.Sprint(1000+i)
			clusters = append(clusters, cluster)
			want[cluster] = rv
			cache.cache[cluster] = resourceVersionClusterCache(func(context.Context) (string, error) {
				return rv, nil
			})
		}

		assert.NoError(t, cache.fillMissingClusterResourceVersion(t.Context(), versions, clusters, podGVR))
		assert.Equal(t, want, versions.rvs)
	})
}
