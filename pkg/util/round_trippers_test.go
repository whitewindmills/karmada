/*
Copyright 2024 The Karmada Authors.

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
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/client-go/transport"
)

func TestProxyHeaderTransportReusesConnections(t *testing.T) {
	var connections atomic.Int32
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			connections.Add(1)
		}
	}
	server.Start()
	defer server.Close()

	base := &http.Transport{}
	defer base.CloseIdleConnections()
	client := &http.Client{
		Transport: NewProxyHeaderRoundTripperWrapperConstructor(nil, map[string]string{
			"Proxy-Authorization": "Basic xyz",
		})(base),
		Timeout: 5 * time.Second,
	}
	defer client.CloseIdleConnections()

	for range 3 {
		resp, err := client.Get(server.URL)
		require.NoError(t, err)
		_, err = io.Copy(io.Discard, resp.Body)
		closeErr := resp.Body.Close()
		require.NoError(t, err)
		require.NoError(t, closeErr)
		require.Equal(t, http.StatusOK, resp.StatusCode)
	}

	assert.Equal(t, int32(1), connections.Load(), "requests should share one persistent connection")
}

func TestProxyHeadersAppliedBeforeWrapping(t *testing.T) {
	base := &http.Transport{
		ProxyConnectHeader: http.Header{"Original": {"unchanged"}},
	}
	var wrappedTransport *http.Transport
	wrapper := func(rt http.RoundTripper) http.RoundTripper {
		var ok bool
		wrappedTransport, ok = rt.(*http.Transport)
		require.True(t, ok)
		return transport.NewUserAgentRoundTripper("proxy-test", rt)
	}

	NewProxyHeaderRoundTripperWrapperConstructor(wrapper, map[string]string{
		"Proxy-Authorization": "Basic xyz",
	})(base)

	require.NotNil(t, wrappedTransport)
	assert.NotSame(t, base, wrappedTransport, "the shared base transport must not be modified")
	assert.Equal(t, http.Header{"Proxy-Authorization": {"Basic xyz"}}, wrappedTransport.ProxyConnectHeader)
	assert.Equal(t, http.Header{"Original": {"unchanged"}}, base.ProxyConnectHeader)
}

func TestProxyHeaderTransportConnect(t *testing.T) {
	for _, withWrapper := range []bool{false, true} {
		name := "without existing wrapper"
		if withWrapper {
			name = "with existing wrapper"
		}
		t.Run(name, func(t *testing.T) {
			connectHeaders := make(chan http.Header, 1)
			proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodConnect {
					connectHeaders <- r.Header.Clone()
				}
				w.WriteHeader(http.StatusBadGateway)
			}))
			defer proxy.Close()
			proxyURL, err := url.Parse(proxy.URL)
			require.NoError(t, err)

			var wrapper transport.WrapperFunc
			if withWrapper {
				wrapper = func(rt http.RoundTripper) http.RoundTripper {
					return transport.NewUserAgentRoundTripper("proxy-test", rt)
				}
			}
			client := &http.Client{
				Transport: NewProxyHeaderRoundTripperWrapperConstructor(wrapper, map[string]string{
					"Proxy-Authorization": "Basic xyz",
				})(&http.Transport{Proxy: http.ProxyURL(proxyURL)}),
				Timeout: 5 * time.Second,
			}
			resp, err := client.Get("https://member.example.invalid")
			if resp != nil {
				defer resp.Body.Close()
			}
			require.Error(t, err, "the test proxy rejects CONNECT after inspecting its headers")
			select {
			case headers := <-connectHeaders:
				assert.Equal(t, "Basic xyz", headers.Get("Proxy-Authorization"))
			default:
				t.Fatal("proxy did not receive a CONNECT request")
			}
		})
	}
}

func TestNewProxyHeaderRoundTripperWrapperConstructor(t *testing.T) {
	tests := []struct {
		name           string
		wrapperFunc    transport.WrapperFunc
		headers        map[string]string
		expectedEmpty  bool
		expectedCount  int
		expectedHeader string
		expectedValues []string
	}{
		{
			name:          "nil wrapper with empty headers",
			wrapperFunc:   nil,
			headers:       nil,
			expectedEmpty: true,
		},
		{
			name:        "nil wrapper with single header",
			wrapperFunc: nil,
			headers: map[string]string{
				"Proxy-Authorization": "Basic xyz",
			},
			expectedCount:  1,
			expectedHeader: "Proxy-Authorization",
			expectedValues: []string{"Basic xyz"},
		},
		{
			name:        "nil wrapper with multiple comma-separated values",
			wrapperFunc: nil,
			headers: map[string]string{
				"X-Custom-Header": "value1,value2,value3",
			},
			expectedCount:  1,
			expectedHeader: "X-Custom-Header",
			expectedValues: []string{"value1", "value2", "value3"},
		},
		{
			name: "with wrapper func",
			wrapperFunc: func(rt http.RoundTripper) http.RoundTripper {
				return rt
			},
			headers: map[string]string{
				"Proxy-Authorization": "Basic abc",
			},
			expectedCount:  1,
			expectedHeader: "Proxy-Authorization",
			expectedValues: []string{"Basic abc"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wrapper := NewProxyHeaderRoundTripperWrapperConstructor(tt.wrapperFunc, tt.headers)
			assert.NotNil(t, wrapper, "wrapper should not be nil")

			rt := wrapper(&http.Transport{})
			tr, ok := rt.(*http.Transport)
			require.True(t, ok, "should return the configured transport")

			if tt.expectedEmpty {
				assert.Empty(t, tr.ProxyConnectHeader, "proxy headers should be empty")
				return
			}

			assert.Equal(t, tt.expectedCount, len(tr.ProxyConnectHeader), "should have expected number of headers")
			assert.Equal(t, tt.expectedValues, tr.ProxyConnectHeader[tt.expectedHeader], "should have expected header values")
		})
	}
}

func TestRoundTrip(t *testing.T) {
	tests := []struct {
		name           string
		roundTripper   http.RoundTripper
		headers        map[string]string
		expectedError  bool
		expectedStatus int
	}{
		{
			name: "with http transport",
			roundTripper: &http.Transport{
				ProxyConnectHeader: make(http.Header),
			},
			headers: map[string]string{
				"Proxy-Authorization": "Basic xyz",
			},
			expectedStatus: http.StatusOK,
		},
		{
			name: "with custom round tripper",
			roundTripper: &mockRoundTripper{
				response: &http.Response{
					StatusCode: http.StatusOK,
				},
			},
			headers: map[string]string{
				"Custom-Header": "value",
			},
			expectedStatus: http.StatusOK,
		},
		{
			name: "with error",
			roundTripper: &mockRoundTripper{
				err: assert.AnError,
			},
			expectedError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rt := NewProxyHeaderRoundTripperWrapperConstructor(nil, tt.headers)(tt.roundTripper)
			if tr, ok := rt.(*http.Transport); ok {
				defer tr.CloseIdleConnections()
			}

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
			}))
			defer server.Close()

			req, err := http.NewRequest(http.MethodGet, server.URL, nil)
			assert.NoError(t, err, "should create request without error")

			resp, err := rt.RoundTrip(req)

			if tt.expectedError {
				assert.Error(t, err, "should return error")
				assert.Nil(t, resp, "response should be nil")
				return
			}

			require.NoError(t, err, "should not return error")
			require.NotNil(t, resp, "response should not be nil")
			if resp.Body != nil {
				defer resp.Body.Close()
			}
			assert.Equal(t, tt.expectedStatus, resp.StatusCode, "should have expected status code")
		})
	}
}

func TestParseProxyHeaders(t *testing.T) {
	tests := []struct {
		name           string
		headers        map[string]string
		expectedEmpty  bool
		expectedCount  int
		expectedHeader string
		expectedValues []string
	}{
		{
			name:          "nil headers",
			headers:       nil,
			expectedEmpty: true,
		},
		{
			name:          "empty headers",
			headers:       map[string]string{},
			expectedEmpty: true,
		},
		{
			name: "single header",
			headers: map[string]string{
				"proxy-authorization": "Basic xyz",
			},
			expectedCount:  1,
			expectedHeader: "Proxy-Authorization",
			expectedValues: []string{"Basic xyz"},
		},
		{
			name: "multiple comma-separated values",
			headers: map[string]string{
				"x-custom-header": "value1,value2,value3",
			},
			expectedCount:  1,
			expectedHeader: "X-Custom-Header",
			expectedValues: []string{"value1", "value2", "value3"},
		},
		{
			name: "multiple headers",
			headers: map[string]string{
				"proxy-authorization": "Basic xyz",
				"x-custom-header":     "value1,value2",
			},
			expectedCount: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := parseProxyHeaders(tt.headers)

			if tt.expectedEmpty {
				assert.Nil(t, result, "headers should be nil")
				return
			}

			assert.Equal(t, tt.expectedCount, len(result), "should have expected number of headers")

			if tt.expectedHeader != "" {
				assert.Equal(t, tt.expectedValues, result[tt.expectedHeader], "should have expected header values")
			}
		})
	}
}

// Mock Implementations

type mockRoundTripper struct {
	response *http.Response
	err      error
}

func (m *mockRoundTripper) RoundTrip(_ *http.Request) (*http.Response, error) {
	return m.response, m.err
}
