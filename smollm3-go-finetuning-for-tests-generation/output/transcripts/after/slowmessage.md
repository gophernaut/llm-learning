# Eval: slowmessage

- Repo: `hashicorp/terraform` `internal/helper/slowmessage/slowmessage.go`
- Ground-truth `go test`: PASS
- Generated `go test`: FAIL
- Generation: 112 tokens in 11.1s

## Extracted Go

```go
package slowmessage

import (
	"testing"
)

func TestDo(t *testing.T) {
	var err error
	threshold := 100 * time.Millisecond
	sf := func() error {
		time.Sleep(threshold)
		return nil
	}
	cb := func() {
		t.Logf("Threshold reached")
	}

	err = Do(threshold, sf, cb)
	if err != nil {
		t.Fatal(err)
	}
}
```

## go test stdout/stderr

```
FAIL	example.com/slowmessage [build failed]
FAIL
# example.com/slowmessage [example.com/slowmessage.test]
./slowmessage_test.go:9:21: undefined: time
./slowmessage_test.go:11:3: undefined: time

```

## Raw model output

```
// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: BUSL-1.1

package slowmessage

import (
	"testing"
)

func TestDo(t *testing.T) {
	var err error
	threshold := 100 * time.Millisecond
	sf := func() error {
		time.Sleep(threshold)
		return nil
	}
	cb := func() {
		t.Logf("Threshold reached")
	}

	err = Do(threshold, sf, cb)
	if err != nil {
		t.Fatal(err)
	}
}

```
