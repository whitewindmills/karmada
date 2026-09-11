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

package helm

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	batchv1 "k8s.io/api/batch/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/yaml"
)

func TestPostDeleteOnlyRemovesReleaseHostResources(t *testing.T) {
	for _, tt := range []struct {
		release         string
		namespace       string
		systemNamespace string
	}{
		{release: "karmada", namespace: "karmada-system", systemNamespace: "karmada-system"},
		{release: "custom-release", namespace: "host-components", systemNamespace: "control-plane-system"},
	} {
		t.Run(tt.release, func(t *testing.T) {
			render := exec.CommandContext(t.Context(), "helm", "template", tt.release, "../../charts/karmada", //nolint:gosec // Arguments are fixed test fixtures and checked-in chart paths.
				"--namespace", tt.namespace, "--set", "systemNamespace="+tt.systemNamespace,
				"--show-only", "templates/pre-install-job.yaml", "--show-only", "templates/post-delete-job.yaml")
			var stderr bytes.Buffer
			render.Stderr = &stderr
			manifest, err := render.Output()
			require.NoError(t, err, "render chart after running helm dependency build charts/karmada: %s", stderr.String())

			wantDeletes := sets.New("deployment/" + tt.release + "-controller-manager")
			var job batchv1.Job
			decoder := yaml.NewYAMLOrJSONDecoder(bytes.NewReader(manifest), 4096)
			for {
				var obj unstructured.Unstructured
				err := decoder.Decode(&obj)
				if errors.Is(err, io.EOF) {
					break
				}
				require.NoError(t, err)
				switch obj.GetKind() {
				case "ConfigMap":
					require.Equal(t, tt.namespace, obj.GetNamespace())
					wantDeletes.Insert("cm/" + obj.GetName())
					if obj.GetName() == tt.release+"-config" {
						data, _, err := unstructured.NestedStringMap(obj.Object, "data")
						require.NoError(t, err)
						require.NotEmpty(t, data)
						for filename, content := range data {
							var blueprint unstructured.Unstructured
							require.NoError(t, yaml.NewYAMLOrJSONDecoder(strings.NewReader(content), 4096).Decode(&blueprint), filename)
							assert.Contains(t, []string{"Secret", "ConfigMap"}, blueprint.GetKind(), filename)
							assert.Equal(t, tt.namespace, blueprint.GetNamespace(), filename)
						}
					}
				case "Job":
					if obj.GetName() == tt.release+"-post-delete" {
						require.NoError(t, runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, &job))
					}
				}
			}
			require.Equal(t, tt.namespace, job.Namespace)
			require.Len(t, job.Spec.Template.Spec.Containers, 1)
			container := job.Spec.Template.Spec.Containers[0]

			// Execute the rendered hook with a recording stub, never a real kubectl.
			tmp := t.TempDir()
			logFile := filepath.Join(tmp, "deletes.log")
			require.NoError(t, os.WriteFile(filepath.Join(tmp, "kubectl"), //nolint:gosec // The recording stub must be executable inside the private test directory.
				[]byte("#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$KUBECTL_DELETE_LOG\"\n"), 0o700))
			require.Len(t, container.Command, 3)
			require.Equal(t, "/bin/sh", container.Command[0])
			run := exec.CommandContext(t.Context(), container.Command[0], container.Command[1:]...) //nolint:gosec // Execute the checked-in hook with kubectl replaced by the test stub.
			run.Env = append(os.Environ(), "PATH="+tmp+string(os.PathListSeparator)+os.Getenv("PATH"), "KUBECTL_DELETE_LOG="+logFile)
			output, err := run.CombinedOutput()
			require.NoError(t, err, "%s", output)
			calls, err := os.ReadFile(logFile)
			require.NoError(t, err)

			gotDeletes := sets.New[string]()
			blueprintDeletes := 0
			for line := range strings.SplitSeq(strings.TrimSpace(string(calls)), "\n") {
				args := strings.Fields(line)
				require.NotEmpty(t, args)
				require.Equal(t, "delete", args[0])
				if len(args) > 1 && args[1] == "-f" {
					assert.Equal(t, []string{"delete", "-f", "/opt/mount/", "--ignore-not-found=true"}, args,
						"control-plane Namespace/CRD/RBAC manifests must never be deleted against the host")
					blueprintDeletes++
					continue
				}
				namespace := ""
				for i := 1; i < len(args); i++ {
					switch args[i] {
					case "-n":
						i++
						require.Less(t, i, len(args))
						namespace = args[i]
					case "--ignore-not-found=true":
					default:
						gotDeletes.Insert(args[i])
					}
				}
				assert.Equal(t, tt.namespace, namespace, line)
			}
			assert.Equal(t, 1, blueprintDeletes)
			assert.Equal(t, wantDeletes, gotDeletes, "clean every hook ConfigMap, not the control-plane manifests it contains")
			require.Len(t, container.VolumeMounts, 1)
			require.Len(t, job.Spec.Template.Spec.Volumes, 1)
			assert.Equal(t, "/opt/mount", container.VolumeMounts[0].MountPath)
			require.NotNil(t, job.Spec.Template.Spec.Volumes[0].ConfigMap)
			assert.Equal(t, tt.release+"-config", job.Spec.Template.Spec.Volumes[0].ConfigMap.Name)
		})
	}
}
