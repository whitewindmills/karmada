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
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/meta"
	metainternalversion "k8s.io/apimachinery/pkg/apis/meta/internalversion"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	genericregistry "k8s.io/apiserver/pkg/registry/generic/registry"
	"k8s.io/apiserver/pkg/storage"
	"k8s.io/client-go/dynamic"
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

type failingKindRESTMapper struct {
	meta.RESTMapper
	calls  int
	failAt int
	err    error
}

func (m *failingKindRESTMapper) KindFor(resource schema.GroupVersionResource) (schema.GroupVersionKind, error) {
	m.calls++
	if m.failAt > 0 && m.calls == m.failAt {
		return schema.GroupVersionKind{}, m.err
	}
	return m.RESTMapper.KindFor(resource)
}

func TestMultiClusterCache_ResourceExpansionInvalidatesWatches(t *testing.T) {
	tests := []struct {
		name      string
		additions []schema.GroupVersionResource
		failAt    int
	}{
		{name: "unchanged resource caches keep watches open"},
		{name: "new resource in an existing cluster", additions: []schema.GroupVersionResource{podGVR}},
		{
			name:      "a later discovery failure does not hide a successful addition",
			additions: []schema.GroupVersionResource{podGVR, secretGVR},
			failAt:    2,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			discoveryErr := errors.New("resource discovery failed")
			mapper := &failingKindRESTMapper{RESTMapper: restMapper, err: discoveryErr}
			client := NewEnhancedFakeDynamicClientWithResourceVersion(scheme, "100")
			cache := NewMultiClusterCache(func(string) (dynamic.Interface, error) { return client, nil }, mapper)
			t.Cleanup(cache.Stop)
			resources := map[string]map[schema.GroupVersionResource]*MultiNamespace{
				"cluster1": resourceSet(podGVR, secretGVR),
				"cluster2": resourceSet(nodeGVR),
			}
			registered := map[schema.GroupVersionResource]struct{}{podGVR: {}, nodeGVR: {}, secretGVR: {}}
			require.NoError(t, cache.UpdateCache(resources, registered))

			var watchers []*watchMuxWithInvalidation
			for _, gvr := range []schema.GroupVersionResource{podGVR, secretGVR} {
				mux := newWatchMuxWithInvalidation()
				mux.AddSource(watch.NewRaceFreeFake(), nil)
				mux.Start()
				cache.registerWatch(gvr, mux)
				t.Cleanup(mux.Stop)
				watchers = append(watchers, mux)
			}

			mapper.calls, mapper.failAt = 0, tt.failAt
			for _, gvr := range tt.additions {
				resources["cluster2"][gvr] = &MultiNamespace{allNamespaces: true}
			}
			err := cache.UpdateCache(resources, registered)
			if tt.failAt > 0 {
				require.ErrorIs(t, err, discoveryErr)
				require.Len(t, cache.cache["cluster2"].cache, 2, "the first new cache must exist before the second discovery fails")
			} else {
				require.NoError(t, err)
			}

			if len(tt.additions) == 0 {
				for _, mux := range watchers {
					select {
					case <-mux.StoppedCh():
						t.Fatal("an unchanged cache must not invalidate an existing watch")
					case <-time.After(50 * time.Millisecond):
					}
				}
				return
			}
			for _, mux := range watchers {
				select {
				case <-mux.StoppedCh():
				case <-time.After(5 * time.Second):
					t.Fatal("a newly added resource cache was not made visible to existing watches")
				}
			}
		})
	}
}

type blockingWatchStorage struct {
	storage.Interface
	started chan struct{}
	release chan struct{}
	source  *watch.RaceFreeFakeWatcher
}

func (s *blockingWatchStorage) Watch(ctx context.Context, _ string, _ storage.ListOptions) (watch.Interface, error) {
	close(s.started)
	select {
	case <-s.release:
		return s.source, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func TestMultiClusterCache_TopologyChangeDuringWatchSetup(t *testing.T) {
	tests := []struct {
		name           string
		resource       schema.GroupVersionResource
		wantInvalidate bool
	}{
		{name: "a new watched source cannot be missed", resource: podGVR, wantInvalidate: true},
		{name: "unrelated resources do not invalidate setup", resource: nodeGVR},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			s := &blockingWatchStorage{
				started: make(chan struct{}),
				release: make(chan struct{}),
				source:  watch.NewRaceFreeFake(),
			}
			client := NewEnhancedFakeDynamicClientWithResourceVersion(scheme, "100")
			cache := NewMultiClusterCache(func(string) (dynamic.Interface, error) { return client, nil }, restMapper)
			cache.cache["cluster1"] = clusterCacheWithStorage(s)
			existing := cache.cache["cluster1"].cache[podGVR]
			existing.multiNS = &MultiNamespace{allNamespaces: true}
			existing.Store.DestroyFunc = s.source.Stop
			t.Cleanup(cache.Stop)

			type watchResult struct {
				watcher watch.Interface
				err     error
			}
			resultCh := make(chan watchResult, 1)
			go func() {
				w, err := cache.Watch(ctx, podGVR, &metainternalversion.ListOptions{})
				resultCh <- watchResult{watcher: w, err: err}
			}()
			select {
			case <-s.started:
			case <-ctx.Done():
				t.Fatal("the original member watch was not started")
			}

			require.NoError(t, cache.UpdateCache(map[string]map[schema.GroupVersionResource]*MultiNamespace{
				"cluster1": resourceSet(podGVR),
				"cluster2": resourceSet(tt.resource),
			}, map[schema.GroupVersionResource]struct{}{podGVR: {}, tt.resource: {}}))
			require.NotNil(t, cache.cacheForClusterResource("cluster2", tt.resource))
			close(s.release)

			var result watchResult
			select {
			case result = <-resultCh:
			case <-ctx.Done():
				t.Fatal("watch setup did not complete")
			}
			require.NoError(t, result.err)
			require.NotNil(t, result.watcher)
			t.Cleanup(result.watcher.Stop)

			if tt.wantInvalidate {
				select {
				case _, ok := <-result.watcher.ResultChan():
					require.False(t, ok, "stale watch setup should reconnect rather than return incomplete events")
				case <-ctx.Done():
					t.Fatal("the watch missed the topology change that happened before registration")
				}
			} else {
				select {
				case <-result.watcher.ResultChan():
					t.Fatal("a change to another resource must not invalidate this watch setup")
				case <-time.After(50 * time.Millisecond):
				}
			}
		})
	}
}
