# Eval: stdlib_http_mapping

- Repo: `golang/go` `src/net/http/mapping.go`
- Ground-truth `go test`: PASS
- Generated `go test`: FAIL
- Generation: 1024 tokens in 72.5s

## Extracted Go

```go
package testing

import (
	"testing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMapping(t *testing.T) {
	m := &mapping{}
	m.add("key1", "value1")
	m.add("key2", "value2")

	// Test find
	t.Run("find", func(t *testing.T) {
		v, found := m.find("key1")
		assert.Equal(t, "value1", v)
		assert.True(t, found)

		v, found = m.find("key3")
		assert.Equal(t, "", v)
		assert.False(t, found)
	})

	// Test eachPair
	t.Run("eachPair", func(t *testing.T) {
		var found bool
		m.eachPair(func(k K, v V) bool {
			found = true
			return true
		})

		assert.True(t, found)

		found = false
		m.eachPair(func(k K, v V) bool {
			found = true
			return false
		})

		assert.True(t, found)
	})

	// Test add with many pairs
	m.add("key3", "value3")
	m.add("key4", "value4")
	m.add("key5", "value5")
	m.add("key6", "value6")
	m.add("key7", "value7")
	m.add("key8", "value8")
	m.add("key9", "value9")
	m.add("key10", "value10")

	// Test find with many pairs
	t.Run("findManyPairs", func(t *testing.T) {
		v, found := m.find("key1")
		assert.Equal(t, "value1", v)
		assert.True(t, found)

		v, found = m.find("key11")
		assert.Equal(t, "", v)
		assert.False(t, found)
	})

	// Test eachPair with many pairs
	t.Run("eachPairManyPairs", func(t *testing.T) {
		var found bool
		m.eachPair(func(k K, v V) bool {
			found = true
			return true
		})

		assert.True(t, found)

		found = false
		m.eachPair(func(k K, v V) bool {
			found = true
			return false
		})

		assert.True(t, found)
	})

	// Test add with many pairs and then switch to slice
	m.add("key11", "value11")
	m.add("key12", "value12")
	m.add("key13", "value13")
	m.add("key14", "value14")
	m.add("key15", "value15")
	m.add("key16", "value16")
	m.add("key17", "value17")
	m.add("key18", "value18")
	m.add("key19", "value19")
	m.add("key20", "value20")

	// Test find with many pairs and then switch to slice
	t.Run("findManyPairsThenSwitch", func(t *testing.T) {
		v, found := m.find("key1")
		assert.Equal(t, "value1", v)
		assert.True(t, found)

		v, found = m.find("key11")
		assert.Equal(t, "value11", v)
		assert.True(t, found)

		v, found = m.find("key21")
		assert.Equal(t, "", v)
		assert.False(t, found)
	})

	// Test eachPair with many pairs and then switch to slice
	t.Run("eachPairManyPairsThenSwitch", func(t *testing.T) {
		var found bool
		m.eachPair(func(k K, v V) bool {
			found = true
			return true
		})

		assert.True(t, found)

		found = false
		m.eachPair(func(k K, v V) bool {
			found = true
			return false
		})

		assert.True(t, found)
	})
}

func BenchmarkAddManyPairs(b *testing.B) {
	m := &mapping{}
	for i := 0; i < 10000; i++ {
		m.add("key" + strconv.Itoa(i), "value" + strconv.Itoa(i))
	}
}

func BenchmarkFindManyPairs(b *testing.B) {
	m := &mapping{}
	for i := 0; i < 10000; i++ {
		m.add("key" + strconv.Itoa(i), "value" + strconv.Itoa(i))
	}
	for i := 0; i < 10000; i++ {
		b.ResetTimer()
		m.find("key" + strconv.Itoa(i))
	}
}
```

## go test stdout/stderr

```
FAIL	example.com/case201 [setup failed]
FAIL
# example.com/case201
found packages http (mapping.go) and testing (mapping_test.go) in /private/var/folders/2q/7mdxl5zd2ms2pb0fdlcbqtgw0000gn/T/tmpbmgwf5fj

```

## Raw model output

```
Here is a Go unit test file for the `mapping` struct in the `http` package:

```go
package testing

import (
	"testing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMapping(t *testing.T) {
	m := &mapping{}
	m.add("key1", "value1")
	m.add("key2", "value2")

	// Test find
	t.Run("find", func(t *testing.T) {
		v, found := m.find("key1")
		assert.Equal(t, "value1", v)
		assert.True(t, found)

		v, found = m.find("key3")
		assert.Equal(t, "", v)
		assert.False(t, found)
	})

	// Test eachPair
	t.Run("eachPair", func(t *testing.T) {
		var found bool
		m.eachPair(func(k K, v V) bool {
			found = true
			return true
		})

		assert.True(t, found)

		found = false
		m.eachPair(func(k K, v V) bool {
			found = true
			return false
		})

		assert.True(t, found)
	})

	// Test add with many pairs
	m.add("key3", "value3")
	m.add("key4", "value4")
	m.add("key5", "value5")
	m.add("key6", "value6")
	m.add("key7", "value7")
	m.add("key8", "value8")
	m.add("key9", "value9")
	m.add("key10", "value10")

	// Test find with many pairs
	t.Run("findManyPairs", func(t *testing.T) {
		v, found := m.find("key1")
		assert.Equal(t, "value1", v)
		assert.True(t, found)

		v, found = m.find("key11")
		assert.Equal(t, "", v)
		assert.False(t, found)
	})

	// Test eachPair with many pairs
	t.Run("eachPairManyPairs", func(t *testing.T) {
		var found bool
		m.eachPair(func(k K, v V) bool {
			found = true
			return true
		})

		assert.True(t, found)

		found = false
		m.eachPair(func(k K, v V) bool {
			found = true
			return false
		})

		assert.True(t, found)
	})

	// Test add with many pairs and then switch to slice
	m.add("key11", "value11")
	m.add("key12", "value12")
	m.add("key13", "value13")
	m.add("key14", "value14")
	m.add("key15", "value15")
	m.add("key16", "value16")
	m.add("key17", "value17")
	m.add("key18", "value18")
	m.add("key19", "value19")
	m.add("key20", "value20")

	// Test find with many pairs and then switch to slice
	t.Run("findManyPairsThenSwitch", func(t *testing.T) {
		v, found := m.find("key1")
		assert.Equal(t, "value1", v)
		assert.True(t, found)

		v, found = m.find("key11")
		assert.Equal(t, "value11", v)
		assert.True(t, found)

		v, found = m.find("key21")
		assert.Equal(t, "", v)
		assert.False(t, found)
	})

	// Test eachPair with many pairs and then switch to slice
	t.Run("eachPairManyPairsThenSwitch", func(t *testing.T) {
		var found bool
		m.eachPair(func(k K, v V) bool {
			found = true
			return true
		})

		assert.True(t, found)

		found = false
		m.eachPair(func(k K, v V) bool {
			found = true
			return false
		})

		assert.True(t, found)
	})
}

func BenchmarkAddManyPairs(b *testing.B) {
	m := &mapping{}
	for i := 0; i < 10000; i++ {
		m.add("key" + strconv.Itoa(i), "value" + strconv.Itoa(i))
	}
}

func BenchmarkFindManyPairs(b *testing.B) {
	m := &mapping{}
	for i := 0; i < 10000; i++ {
		m.add("key" + strconv.Itoa(i), "value" + strconv.Itoa(i))
	}
	for i := 0; i < 10000; i++ {
		b.ResetTimer()
		m.find("key" + strconv.Itoa(i))
	}
}
```

This test suite covers various scenarios for the `mapping` struct, including adding and finding pairs, iterating over pairs, and handling cases where the number of
```
