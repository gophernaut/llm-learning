# Eval: iotest_writer

- Repo: `golang/go` `src/testing/iotest/writer.go`
- Ground-truth `go test`: PASS
- Generated `go test`: FAIL
- Generation: 254 tokens in 24.6s

## Extracted Go

```go
package iotest

import (
	"bytes"
	"testing"
)

func TestTruncateWriter(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		w := TruncateWriter(io.NopCloser(bytes.NewBuffer([]byte{})), 0)
		_ = w.Write([]byte{})
	})

	t.Run("full", func(t *testing.T) {
		w := TruncateWriter(io.NopCloser(bytes.NewBuffer([]byte{})), 10)
		_ = w.Write([]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10})
	})

	t.Run("partial", func(t *testing.T) {
		w := TruncateWriter(io.NopCloser(bytes.NewBuffer([]byte{})), 5)
		_ = w.Write([]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10})
	})
}
```

## go test stdout/stderr

```
FAIL	example.com/iotest [build failed]
FAIL
# example.com/iotest [example.com/iotest.test]
./writer_test.go:10:23: undefined: io
./writer_test.go:11:7: assignment mismatch: 1 variable but w.Write returns 2 values
./writer_test.go:15:23: undefined: io
./writer_test.go:16:7: assignment mismatch: 1 variable but w.Write returns 2 values
./writer_test.go:20:23: undefined: io
./writer_test.go:21:7: assignment mismatch: 1 variable but w.Write returns 2 values

```

## Raw model output

```
// Copyright 2009 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package iotest

import (
	"bytes"
	"testing"
)

func TestTruncateWriter(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		w := TruncateWriter(io.NopCloser(bytes.NewBuffer([]byte{})), 0)
		_ = w.Write([]byte{})
	})

	t.Run("full", func(t *testing.T) {
		w := TruncateWriter(io.NopCloser(bytes.NewBuffer([]byte{})), 10)
		_ = w.Write([]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10})
	})

	t.Run("partial", func(t *testing.T) {
		w := TruncateWriter(io.NopCloser(bytes.NewBuffer([]byte{})), 5)
		_ = w.Write([]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10})
	})
}

```
