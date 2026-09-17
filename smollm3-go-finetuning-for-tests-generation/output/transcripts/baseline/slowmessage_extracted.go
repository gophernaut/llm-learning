package slowmessage_test

import (
	"testing"
	"time"
)

func TestDo(t *testing.T) {
	// Create a mock slow function that takes a long time to complete
	mockSlowFunc := func() error {
		time.Sleep(2 * time.Second)
		return nil
	}

	// Create a mock callback function
	mockCallbackFunc := func() {}

	// Test case 1: Threshold is reached before the slow function completes
	t.Run("ThresholdReached", func(t *testing.T) {
		threshold := time.Second
		err := Do(threshold, mockSlowFunc, mockCallbackFunc)
		if err != nil {
			t.Errorf("Expected no error, got %v", err)
		}
	})

	// Test case 2: Slow function completes before the threshold is reached
	t.Run("SlowFunctionCompletes", func(t *testing.T) {
		threshold := time.Millisecond
		err := Do(threshold, mockSlowFunc, mockCallbackFunc)
		if err != nil {
			t.Errorf("Expected no error, got %v", err)
		}
	})
}