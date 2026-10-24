package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestStallAfterDefaultsToNinetySeconds(t *testing.T) {
	t.Setenv("STALL_AFTER", "")
	require.Equal(t, 90*time.Second, Load().StallAfter)
	t.Setenv("STALL_AFTER", "2s")
	require.Equal(t, 2*time.Second, Load().StallAfter)
}
