# Eval: stdlib_zstd_window

- Repo: `golang/go` `src/internal/zstd/window.go`
- Ground-truth `go test`: PASS
- Generated `go test`: FAIL
- Generation: 1024 tokens in 73.4s

## Extracted Go

```go
package zstd

import (
	"testing"
)

func TestWindow(t *testing.T) {
	w := &window{}
	w.reset(10)

	// Test reset
	if w.size != 10 {
		t.Errorf("window size not set to 10")
	}
	if w.off != 0 {
		t.Errorf("window offset not set to 0")
	}
	if len(w.data) != 0 {
		t.Errorf("window data not cleared")
	}

	// Test save
	buf := make([]byte, 10)
	for i := 0; i < 10; i++ {
		buf[i] = byte(i)
	}
	w.save(buf)
	if len(w.data) != 10 {
		t.Errorf("window data length not 10")
	}
	if w.off != 0 {
		t.Errorf("window offset not 0")
	}
	for i := 0; i < 10; i++ {
		if w.data[i] != byte(10 - i) {
			t.Errorf("window data[%d] not %d", i, byte(10 - i))
		}
	}

	// Test appendTo
	buf = make([]byte, 10)
	for i := 0; i < 10; i++ {
		buf[i] = byte(i)
	}
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	if len(w.data) != 10 {
		t.Errorf("window data length not 10")
	}
	if w.off != 0 {
		t.Errorf("window offset not 0")
	}
	for i := 0; i < 10; i++ {
		if w.data[i] != byte(i) {
			t.Errorf("window data[%d] not %d", i, byte(i))
		}
	}

	// Test appendTo with wrap
	buf = make([]byte, 10)
	for i := 0; i < 10; i++ {
		buf[i] = byte(i)
	}
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save
```

## go test stdout/stderr

```
FAIL	example.com/case341 [setup failed]
FAIL
# example.com/case341
window_test.go:211:8: expected '}', found 'EOF'

```

## Raw model output

```
Here is a Go unit test file for the `window` struct in the `zstd` package:

```go
package zstd

import (
	"testing"
)

func TestWindow(t *testing.T) {
	w := &window{}
	w.reset(10)

	// Test reset
	if w.size != 10 {
		t.Errorf("window size not set to 10")
	}
	if w.off != 0 {
		t.Errorf("window offset not set to 0")
	}
	if len(w.data) != 0 {
		t.Errorf("window data not cleared")
	}

	// Test save
	buf := make([]byte, 10)
	for i := 0; i < 10; i++ {
		buf[i] = byte(i)
	}
	w.save(buf)
	if len(w.data) != 10 {
		t.Errorf("window data length not 10")
	}
	if w.off != 0 {
		t.Errorf("window offset not 0")
	}
	for i := 0; i < 10; i++ {
		if w.data[i] != byte(10 - i) {
			t.Errorf("window data[%d] not %d", i, byte(10 - i))
		}
	}

	// Test appendTo
	buf = make([]byte, 10)
	for i := 0; i < 10; i++ {
		buf[i] = byte(i)
	}
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	if len(w.data) != 10 {
		t.Errorf("window data length not 10")
	}
	if w.off != 0 {
		t.Errorf("window offset not 0")
	}
	for i := 0; i < 10; i++ {
		if w.data[i] != byte(i) {
			t.Errorf("window data[%d] not %d", i, byte(i))
		}
	}

	// Test appendTo with wrap
	buf = make([]byte, 10)
	for i := 0; i < 10; i++ {
		buf[i] = byte(i)
	}
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save(buf)
	w.save
```
