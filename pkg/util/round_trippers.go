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

package util

import (
	"net/http"
	"net/textproto"
	"strings"

	"k8s.io/client-go/transport"
)

// NewProxyHeaderRoundTripperWrapperConstructor returns a RoundTripper wrapper that's usable within restConfig.WrapTransport.
// It configures a private transport once so requests can reuse its connections.
func NewProxyHeaderRoundTripperWrapperConstructor(wt transport.WrapperFunc, headers map[string]string) transport.WrapperFunc {
	return func(rt http.RoundTripper) http.RoundTripper {
		if tr, ok := rt.(*http.Transport); ok {
			tr = tr.Clone()
			tr.ProxyConnectHeader = parseProxyHeaders(headers)
			rt = tr
		}
		if wt != nil {
			rt = wt(rt)
		}
		return rt
	}
}

func parseProxyHeaders(headers map[string]string) http.Header {
	if len(headers) == 0 {
		return nil
	}

	proxyHeaders := make(http.Header)
	for key, values := range headers {
		proxyHeaders[textproto.CanonicalMIMEHeaderKey(key)] = strings.Split(values, ",")
	}
	return proxyHeaders
}
