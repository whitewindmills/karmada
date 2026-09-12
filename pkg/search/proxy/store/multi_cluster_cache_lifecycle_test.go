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
	metainternalversion "k8s.io/apimachinery/pkg/apis/meta/internalversion"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
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

func clusterCacheWithStorage(s storage.Interface) *clusterCache {
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
						Storage: s,
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
			cache.cache[cluster] = clusterCacheWithStorage(&resourceVersionStorage{fetch: func(context.Context) (string, error) {
				return "", listErr
			}})
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
		cache.cache["failed"] = clusterCacheWithStorage(&resourceVersionStorage{fetch: func(context.Context) (string, error) {
			<-pendingStarted
			return "", listErr
		}})
		cache.cache["pending"] = clusterCacheWithStorage(&resourceVersionStorage{fetch: func(ctx context.Context) (string, error) {
			close(pendingStarted)
			<-ctx.Done()
			close(pendingFinished)
			return "", ctx.Err()
		}})

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
		cache.cache["existing"] = clusterCacheWithStorage(&resourceVersionStorage{fetch: func(context.Context) (string, error) {
			return "", errors.New("an existing resource version must not be fetched again")
		}})
		for i := range 32 {
			cluster, rv := fmt.Sprintf("cluster%d", i), fmt.Sprint(1000+i)
			clusters = append(clusters, cluster)
			want[cluster] = rv
			cache.cache[cluster] = clusterCacheWithStorage(&resourceVersionStorage{fetch: func(context.Context) (string, error) {
				return rv, nil
			}})
		}

		assert.NoError(t, cache.fillMissingClusterResourceVersion(t.Context(), versions, clusters, podGVR))
		assert.Equal(t, want, versions.rvs)
	})
}

type watchSetupStorage struct {
	storage.Interface
	started   []*watch.RaceFreeFakeWatcher
	failAfter int
	err       error
}

func (s *watchSetupStorage) Watch(context.Context, string, storage.ListOptions) (watch.Interface, error) {
	if len(s.started) == s.failAfter {
		return nil, s.err
	}
	w := watch.NewRaceFreeFake()
	s.started = append(s.started, w)
	return w, nil
}

func TestMultiClusterCache_WatchSetupFailureStopsStartedSources(t *testing.T) {
	for failAfter := range 3 {
		t.Run(fmt.Sprintf("after %d sources", failAfter), func(t *testing.T) {
			watchErr := errors.New("member watch failed")
			s := &watchSetupStorage{failAfter: failAfter, err: watchErr}
			t.Cleanup(func() {
				for _, source := range s.started {
					source.Stop()
				}
			})
			cache := NewMultiClusterCache(nil, nil)
			for i := range 3 {
				cache.cache[fmt.Sprintf("cluster%d", i)] = clusterCacheWithStorage(s)
			}

			w, err := cache.Watch(t.Context(), podGVR, &metainternalversion.ListOptions{})
			assert.ErrorIs(t, err, watchErr)
			assert.Nil(t, w)
			assert.Len(t, s.started, failAfter)
			for _, source := range s.started {
				assert.True(t, source.IsStopped(), "a failed watch setup must release every previously started member watch")
			}
			assert.Empty(t, cache.activeWatchers)
		})
	}
}
