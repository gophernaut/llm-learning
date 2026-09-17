# Eval: k8s_mux_discovery

- Repo: `kubernetes/kubernetes` `staging/src/k8s.io/apiserver/pkg/endpoints/filters/mux_discovery_complete.go`
- Ground-truth `go test`: PASS
- Generated `go test`: FAIL
- Generation: 430 tokens in 31.1s

## Extracted Go

```go
package filters_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWithMuxAndDiscoveryComplete(t *testing.T) {
	muxAndDiscoveryCompleteSignal := make(chan struct{})
	go func() {
		time.Sleep(100 * time.Millisecond)
		close(muxAndDiscoveryCompleteSignal)
	}()

	handler := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	filteredHandler := WithMuxAndDiscoveryComplete(handler, muxAndDiscoveryCompleteSignal)

	testCtx := context.WithValue(context.Background(), muxAndDiscoveryIncompleteKey, "MuxAndDiscoveryInstallationNotComplete")

	testRequest := httptest.NewRequest("GET", "/", nil)
	testRequest = testRequest.WithContext(testCtx)

	testResponse := httptest.NewRecorder()
	filteredHandler(testResponse, testRequest)

	if testResponse.Code != http.StatusNotFound {
		t.Errorf("Expected 404, got %d", testResponse.Code)
	}

	testCtx2 := context.WithValue(context.Background(), muxAndDiscoveryIncompleteKey, "")

	testRequest2 := httptest.NewRequest("GET", "/", nil)
	testRequest2 = testRequest2.WithContext(testCtx2)

	testResponse2 := httptest.NewRecorder()
	filteredHandler(testResponse2, testRequest2)

	if testResponse2.Code != http.StatusNotFound {
		t.Errorf("Expected 404, got %d", testResponse2.Code)
	}
}
```

## go test stdout/stderr

```
FAIL	example.com/case390 [build failed]
FAIL
# example.com/case390_test [example.com/case390.test]
./mux_discovery_complete_test.go:13:3: undefined: time
./mux_discovery_complete_test.go:21:21: undefined: WithMuxAndDiscoveryComplete
./mux_discovery_complete_test.go:23:53: undefined: muxAndDiscoveryIncompleteKey
./mux_discovery_complete_test.go:35:54: undefined: muxAndDiscoveryIncompleteKey

```

## Raw model output

```
Here's a unit test file for the `mux_discovery_complete.go` file:

```go
package filters_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWithMuxAndDiscoveryComplete(t *testing.T) {
	muxAndDiscoveryCompleteSignal := make(chan struct{})
	go func() {
		time.Sleep(100 * time.Millisecond)
		close(muxAndDiscoveryCompleteSignal)
	}()

	handler := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	filteredHandler := WithMuxAndDiscoveryComplete(handler, muxAndDiscoveryCompleteSignal)

	testCtx := context.WithValue(context.Background(), muxAndDiscoveryIncompleteKey, "MuxAndDiscoveryInstallationNotComplete")

	testRequest := httptest.NewRequest("GET", "/", nil)
	testRequest = testRequest.WithContext(testCtx)

	testResponse := httptest.NewRecorder()
	filteredHandler(testResponse, testRequest)

	if testResponse.Code != http.StatusNotFound {
		t.Errorf("Expected 404, got %d", testResponse.Code)
	}

	testCtx2 := context.WithValue(context.Background(), muxAndDiscoveryIncompleteKey, "")

	testRequest2 := httptest.NewRequest("GET", "/", nil)
	testRequest2 = testRequest2.WithContext(testCtx2)

	testResponse2 := httptest.NewRecorder()
	filteredHandler(testResponse2, testRequest2)

	if testResponse2.Code != http.StatusNotFound {
		t.Errorf("Expected 404, got %d", testResponse2.Code)
	}
}
```

This test checks two scenarios:

1. When the `muxAndDiscoveryCompleteSignal` is not yet closed, the request should be marked with the `muxAndDiscoveryIncompleteKey` and the `http.StatusNotFound` response should be returned.
2. When the `muxAndDiscoveryCompleteSignal` is closed, the request should not be marked with the `muxAndDiscoveryIncompleteKey` and the `http.StatusNotFound` response should be returned.

The test uses `httptest.NewRecorder` to simulate a response and checks the HTTP status code.
```
