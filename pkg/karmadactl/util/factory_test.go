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
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	clusterv1alpha1 "github.com/karmada-io/karmada/pkg/apis/cluster/v1alpha1"
)

func TestMemberFactoryInheritsConnectionFlags(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(&clusterv1alpha1.Cluster{ObjectMeta: metav1.ObjectMeta{Name: "member"}}); err != nil {
			t.Errorf("failed to write test cluster: %v", err)
		}
	}))
	defer server.Close()
	caData := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	apiServer := strings.Replace(server.URL, "127.0.0.1", "localhost", 1)
	for _, disableCompression := range []bool{false, true} {
		t.Run(fmt.Sprintf("disableCompression=%t", disableCompression), func(t *testing.T) {
			configPath := filepath.Join(t.TempDir(), "kubeconfig")
			config := clientcmdapi.Config{
				Clusters: map[string]*clientcmdapi.Cluster{
					"karmada": {Server: apiServer, CertificateAuthorityData: caData, TLSServerName: "wrong.invalid", DisableCompression: !disableCompression},
				},
				AuthInfos:      map[string]*clientcmdapi.AuthInfo{"user": {}},
				Contexts:       map[string]*clientcmdapi.Context{"test": {Cluster: "karmada", AuthInfo: "user"}},
				CurrentContext: "test",
			}
			if err := clientcmd.WriteToFile(config, configPath); err != nil {
				t.Fatal(err)
			}
			flags := genericclioptions.NewConfigFlags(false)
			flags.KubeConfig = &configPath
			flags.TLSServerName = &server.Certificate().DNSNames[0]
			flags.DisableCompression = &disableCompression
			flags.Impersonate = new("test-user")
			extra := []string{"project=cli"}
			flags.ImpersonateUserExtra = &extra
			factory := NewFactory(flags)
			callerConfig, err := factory.ToRESTConfig()
			if err != nil {
				t.Fatal(err)
			}
			member, err := factory.FactoryForMemberCluster("member")
			if err != nil {
				t.Fatal(err)
			}
			memberConfig, err := member.ToRESTConfig()
			if err != nil {
				t.Fatal(err)
			}
			if memberConfig.ServerName != *flags.TLSServerName {
				t.Errorf("member TLS server name = %q, want %q", memberConfig.ServerName, *flags.TLSServerName)
			}
			if memberConfig.DisableCompression != callerConfig.DisableCompression {
				t.Errorf("member compression setting = %v, want caller's %v", memberConfig.DisableCompression, callerConfig.DisableCompression)
			}
			if !reflect.DeepEqual(memberConfig.Impersonate.Extra, map[string][]string{"project": {"cli"}}) {
				t.Errorf("member impersonation extras = %v, want project=cli", memberConfig.Impersonate.Extra)
			}
			client, err := rest.HTTPClientFor(memberConfig)
			if err != nil {
				t.Fatal(err)
			}
			response, err := client.Get(memberConfig.Host)
			if err != nil {
				t.Fatalf("member proxy connection failed despite valid caller TLS flags: %v", err)
			}
			defer response.Body.Close()
			if response.StatusCode != http.StatusOK {
				t.Errorf("member proxy returned HTTP %d", response.StatusCode)
			}
		})
	}
}
