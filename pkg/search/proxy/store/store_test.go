/*
Copyright 2023 The Karmada Authors.

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
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metainternalversion "k8s.io/apimachinery/pkg/apis/meta/internalversion"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/apiserver/pkg/storage"
	"k8s.io/client-go/dynamic"
	fakedynamic "k8s.io/client-go/dynamic/fake"
	kubetesting "k8s.io/client-go/testing"
)

func TestResourceCacheExactNameScope(t *testing.T) {
	tests := []struct {
		gvr       schema.GroupVersionResource
		gvk       schema.GroupVersionKind
		namespace string
	}{
		{gvr: nodeGVR, gvk: nodeGVK},
		{gvr: schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}, gvk: schema.GroupVersionKind{Version: "v1", Kind: "Namespace"}},
		{gvr: schema.GroupVersionResource{Group: "example.com", Version: "v1", Resource: "widgets"}, gvk: schema.GroupVersionKind{Group: "example.com", Version: "v1", Kind: "Widget"}},
		{gvr: podGVR, gvk: podGVK, namespace: "test"},
	}
	for _, tt := range tests {
		t.Run(tt.gvr.Resource, func(t *testing.T) {
			obj := &unstructured.Unstructured{}
			obj.SetGroupVersionKind(tt.gvk)
			obj.SetName("target")
			obj.SetNamespace(tt.namespace)
			obj.SetResourceVersion("10")
			client := fakedynamic.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
				map[schema.GroupVersionResource]string{tt.gvr: tt.gvk.Kind + "List"}, obj)
			client.PrependReactor("list", tt.gvr.Resource, func(action kubetesting.Action) (bool, runtime.Object, error) {
				if action.GetNamespace() != "" && action.GetNamespace() != tt.namespace {
					return true, nil, apierrors.NewNotFound(tt.gvr.GroupResource(), "")
				}
				list := &unstructured.UnstructuredList{}
				list.SetGroupVersionKind(tt.gvk.GroupVersion().WithKind(tt.gvk.Kind + "List"))
				list.SetResourceVersion("10")
				selector := action.(kubetesting.ListAction).GetListRestrictions().Fields
				if _, exactName := selector.RequiresExactMatch("metadata.name"); exactName {
					assert.Equal(t, tt.namespace, action.GetNamespace())
				}
				for _, name := range []string{"target", "other"} {
					if selector.Matches(fields.Set{"metadata.name": name, "metadata.namespace": tt.namespace}) {
						item := obj.DeepCopy()
						item.SetName(name)
						list.Items = append(list.Items, *item)
					}
				}
				return true, list, nil
			})
			client.PrependWatchReactor(tt.gvr.Resource, func(action kubetesting.Action) (bool, watch.Interface, error) {
				if action.GetNamespace() != "" && action.GetNamespace() != tt.namespace {
					return true, nil, apierrors.NewNotFound(tt.gvr.GroupResource(), "")
				}
				selector := action.(kubetesting.WatchAction).GetWatchRestrictions().Fields
				if _, exactName := selector.RequiresExactMatch("metadata.name"); exactName {
					assert.Equal(t, tt.namespace, action.GetNamespace())
				}
				watcher := watch.NewRaceFreeFake()
				watcher.Add(obj.DeepCopy())
				return true, watcher, nil
			})
			resourceCache, err := newResourceCache("member", tt.gvr, tt.gvk, strings.ToLower(tt.gvk.Kind),
				tt.namespace != "", &MultiNamespace{allNamespaces: true},
				func() (dynamic.NamespaceableResourceInterface, error) { return client.Resource(tt.gvr), nil })
			require.NoError(t, err)
			t.Cleanup(resourceCache.Store.DestroyFunc)
			ctx := request.WithNamespace(context.Background(), tt.namespace)
			selector := fields.OneTermEqualSelector("metadata.name", obj.GetName())

			t.Run("get", func(t *testing.T) {
				got, err := resourceCache.Get(ctx, obj.GetName(), &metav1.GetOptions{})
				require.NoError(t, err)
				assert.Equal(t, obj.GetName(), got.(*unstructured.Unstructured).GetName())
			})
			for _, listTest := range []struct {
				name     string
				selector fields.Selector
				limit    int64
				count    int
			}{
				{name: "all", selector: fields.Everything(), count: 2},
				{name: "exact name", selector: selector, count: 1},
				{name: "paginated exact name", selector: selector, limit: 1, count: 1},
			} {
				t.Run(listTest.name, func(t *testing.T) {
					got, err := resourceCache.List(ctx, &metainternalversion.ListOptions{
						FieldSelector: listTest.selector,
						Limit:         listTest.limit,
					})
					require.NoError(t, err)
					items := got.(*unstructured.UnstructuredList).Items
					require.Len(t, items, listTest.count)
					assert.Equal(t, obj.GetName(), items[0].GetName())
				})
			}
			t.Run("underlying exact name watch", func(t *testing.T) {
				key, err := resourceCache.KeyFunc(ctx, obj.GetName())
				require.NoError(t, err)
				backingStore := resourceCache.Storage.Storage.(*CacheDelegator).storage
				watcher, err := backingStore.Watch(ctx, key, storage.ListOptions{
					Predicate: storage.SelectionPredicate{Label: labels.Everything(), Field: selector},
				})
				require.NoError(t, err)
				defer watcher.Stop()
				select {
				case event := <-watcher.ResultChan():
					require.Equal(t, watch.Added, event.Type)
					assert.Equal(t, obj.GetName(), event.Object.(*unstructured.Unstructured).GetName())
				case <-time.After(time.Second):
					t.Fatal("exact-name watch did not return the matching object")
				}
			})
		})
	}
}

func Test_filterNS(t *testing.T) {
	type args struct {
		cached  *MultiNamespace
		request string
	}
	tests := []struct {
		name             string
		args             args
		wantReqNS        string
		wantObjFilter    bool
		wantShortCircuit bool
	}{
		{
			name: "Cache all namespaces, and request NamespaceAll",
			args: args{
				cached:  &MultiNamespace{allNamespaces: true},
				request: metav1.NamespaceAll,
			},
			wantReqNS:        metav1.NamespaceAll,
			wantObjFilter:    false,
			wantShortCircuit: false,
		},
		{
			name: "Cache all namespaces, and request foo ns",
			args: args{
				cached:  &MultiNamespace{allNamespaces: true},
				request: "foo",
			},
			wantReqNS:        "foo",
			wantObjFilter:    false,
			wantShortCircuit: false,
		},
		{
			name: "Cache foo namespace, and request all namespaces",
			args: args{
				cached:  &MultiNamespace{namespaces: sets.New[string]("foo")},
				request: metav1.NamespaceAll,
			},
			wantReqNS:        "foo",
			wantObjFilter:    false,
			wantShortCircuit: false,
		},
		{
			name: "Cache foo namespace, and request foo ns",
			args: args{
				cached:  &MultiNamespace{namespaces: sets.New[string]("foo")},
				request: "foo",
			},
			wantReqNS:        "foo",
			wantObjFilter:    false,
			wantShortCircuit: false,
		},
		{
			name: "Cache foo namespace, and request bar ns",
			args: args{
				cached:  &MultiNamespace{namespaces: sets.New[string]("foo")},
				request: "bar",
			},
			wantReqNS:        "",
			wantObjFilter:    false,
			wantShortCircuit: true,
		},
		{
			name: "Cache foo,bar namespaces, and request all namespaces",
			args: args{
				cached:  &MultiNamespace{namespaces: sets.New[string]("foo", "bar")},
				request: metav1.NamespaceAll,
			},
			wantReqNS:        metav1.NamespaceAll,
			wantObjFilter:    true,
			wantShortCircuit: false,
		},
		{
			name: "Cache foo,bar namespaces, and request foo namespace",
			args: args{
				cached:  &MultiNamespace{namespaces: sets.New[string]("foo", "bar")},
				request: "foo",
			},
			wantReqNS:        "foo",
			wantObjFilter:    false,
			wantShortCircuit: false,
		},
		{
			name: "Cache foo,bar namespaces, and request baz namespace",
			args: args{
				cached:  &MultiNamespace{namespaces: sets.New[string]("foo", "bar")},
				request: "baz",
			},
			wantReqNS:        "",
			wantObjFilter:    false,
			wantShortCircuit: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotReqNS, gotObjFilter, gotShortCircuit := filterNS(tt.args.cached, tt.args.request)
			if gotReqNS != tt.wantReqNS {
				t.Errorf("filterNS() gotReqNS = %v, want %v", gotReqNS, tt.wantReqNS)
			}
			if (gotObjFilter != nil) != tt.wantObjFilter {
				t.Errorf("filterNS() gotObjFilter %v, want %v", gotObjFilter != nil, tt.wantObjFilter)
			}
			if gotShortCircuit != tt.wantShortCircuit {
				t.Errorf("filterNS() gotShortCircuit = %v, want %v", gotShortCircuit, tt.wantShortCircuit)
			}
		})
	}
}
