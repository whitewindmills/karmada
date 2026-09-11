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

package util

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"k8s.io/client-go/rest"

	workv1alpha1 "github.com/karmada-io/karmada/pkg/apis/work/v1alpha1"
	karmadaclientset "github.com/karmada-io/karmada/pkg/generated/clientset/versioned"
)

func TestCleanupPollingCancelsAPIRequests(t *testing.T) {
	const timeout = 1500 * time.Millisecond
	for _, tt := range []struct {
		name        string
		blockMethod string
		run         func(karmadaclientset.Interface) error
	}{
		{
			name:        "waiting for Works",
			blockMethod: http.MethodGet,
			run: func(client karmadaclientset.Interface) error {
				return EnsureWorksDeleted(client, "karmada-es-member", timeout)
			},
		},
		{
			name:        "waiting for Cluster deletion",
			blockMethod: http.MethodGet,
			run: func(client karmadaclientset.Interface) error {
				return DeleteClusterObject(nil, client, "member", timeout, false, false)
			},
		},
		{
			name:        "deleting a Work",
			blockMethod: http.MethodDelete,
			run: func(client karmadaclientset.Interface) error {
				return EnsureWorksDeleted(client, "karmada-es-member", timeout)
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			requestStarted := make(chan struct{})
			handlerContext, cancelHandlers := context.WithCancel(context.Background())
			var once sync.Once
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if _, err := io.Copy(io.Discard, r.Body); err != nil {
					t.Errorf("failed to read test API request: %v", err)
					return
				}
				if r.Method != tt.blockMethod {
					if r.Method == http.MethodDelete {
						w.WriteHeader(http.StatusOK)
						return
					}
					w.Header().Set("Content-Type", "application/json")
					list := &workv1alpha1.WorkList{Items: []workv1alpha1.Work{{}}}
					list.Items[0].Name = "work"
					if err := json.NewEncoder(w).Encode(list); err != nil {
						t.Errorf("failed to write test API response: %v", err)
					}
					return
				}
				once.Do(func() { close(requestStarted) })
				select {
				case <-r.Context().Done():
				case <-handlerContext.Done():
				}
			}))
			defer server.Close()
			defer server.CloseClientConnections()
			defer cancelHandlers()
			client, err := karmadaclientset.NewForConfig(&rest.Config{Host: server.URL, Timeout: 5 * time.Second})
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- tt.run(client) }()
			select {
			case err := <-done:
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Errorf("cleanup error = %v, want context deadline exceeded", err)
				}
			case <-time.After(4 * time.Second):
				t.Error("API request outlived the cleanup deadline")
				cancelHandlers()
				server.CloseClientConnections()
				server.Close()
				select {
				case <-done:
				case <-time.After(3 * time.Second):
					t.Fatal("cleanup did not exit after the test server closed")
				}
			}
			select {
			case <-requestStarted:
			default:
				t.Error("the test did not exercise an in-flight API request")
			}
		})
	}
}
