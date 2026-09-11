/*
Copyright 2025 The Karmada Authors.

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

package helper

import (
	"bytes"
	"fmt"

	"k8s.io/apimachinery/pkg/util/json"
	"k8s.io/client-go/util/jsonpath"
	"k8s.io/klog/v2"

	policyv1alpha1 "github.com/karmada-io/karmada/pkg/apis/policy/v1alpha1"
)

// BuildPreservedLabelState builds preserved label state from the state preservation rules and raw status.
func BuildPreservedLabelState(statePreservation *policyv1alpha1.StatePreservation, rawStatus []byte) (map[string]string, error) {
	results := make(map[string]string, len(statePreservation.Rules))
	if len(statePreservation.Rules) == 0 {
		return results, nil
	}
	var status any
	// Preserve integer state such as checkpoint IDs without float64 rounding.
	if err := json.Unmarshal(rawStatus, &status); err != nil {
		klog.ErrorS(err, "Failed to unmarshal application status", "status", string(rawStatus))
		return nil, fmt.Errorf("failed to unmarshal rawStatus: %w", err)
	}
	for _, rule := range statePreservation.Rules {
		value, err := parseJSONValue(status, rule.JSONPath)
		if err != nil {
			klog.ErrorS(err, "Failed to parse value with jsonPath from status",
				"jsonPath", rule.JSONPath,
				"status", string(rawStatus))
			return nil, err
		}
		results[rule.AliasLabelName] = value
	}

	return results, nil
}

func parseJSONValue(status any, jsonPath string) (string, error) {
	j := jsonpath.New(jsonPath)
	j.AllowMissingKeys(false)
	if err := j.Parse(jsonPath); err != nil {
		return "", err
	}
	buf := new(bytes.Buffer)
	if err := j.Execute(buf, status); err != nil {
		return "", fmt.Errorf("failed to execute jsonpath %q: %w", jsonPath, err)
	}
	return buf.String(), nil
}
