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

package hpascaletargetmarker

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	controllerruntime "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestIndexHPAScaleTarget(t *testing.T) {
	tests := []struct {
		name       string
		apiVersion string
		kind       string
		target     string
		wantKey    string
	}{
		{name: "stable version", apiVersion: "example.com/v1", kind: "Worker", target: "target", wantKey: "example.com/Worker/target"},
		{name: "another served version", apiVersion: "example.com/v1beta1", kind: "Worker", target: "target", wantKey: "example.com/Worker/target"},
		{name: "other group", apiVersion: "other.example.com/v1", kind: "Worker", target: "target", wantKey: "other.example.com/Worker/target"},
		{name: "other kind", apiVersion: "example.com/v1", kind: "OtherWorker", target: "target", wantKey: "example.com/OtherWorker/target"},
		{name: "other name", apiVersion: "example.com/v1", kind: "Worker", target: "other", wantKey: "example.com/Worker/other"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hpa := propagatedHPA()
			hpa.Spec.ScaleTargetRef = autoscalingv2.CrossVersionObjectReference{
				APIVersion: tt.apiVersion, Kind: tt.kind, Name: tt.target,
			}
			require.Equal(t, []string{tt.wantKey}, indexHPAScaleTarget(hpa))
		})
	}
}

type indexErrorManager struct {
	controllerruntime.Manager
	indexer client.FieldIndexer
}

func (m *indexErrorManager) GetFieldIndexer() client.FieldIndexer {
	return m.indexer
}

type failingHPAIndexer struct {
	err error
}

func (i *failingHPAIndexer) IndexField(context.Context, client.Object, string, client.IndexerFunc) error {
	return i.err
}

func TestSetupWithManagerReturnsIndexErrors(t *testing.T) {
	indexErr := errors.New("cannot register scale target index")
	marker := &HpaScaleTargetMarker{}
	mgr := &indexErrorManager{indexer: &failingHPAIndexer{err: indexErr}}

	require.ErrorIs(t, marker.SetupWithManager(mgr), indexErr)
	require.Nil(t, marker.scaleTargetWorker, "the worker must not start without its ownership index")
}
