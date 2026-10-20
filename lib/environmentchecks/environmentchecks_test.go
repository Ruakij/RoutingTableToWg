package environmentchecks

import (
	"os"
	"testing"
)

func TestHandleDefaults(t *testing.T) {
	t.Setenv("TEST_UNSET", "")
	os.Unsetenv("TEST_UNSET")
	t.Setenv("TEST_EMPTY", "")
	t.Setenv("TEST_SET", "value")

	HandleDefaults(map[string]string{
		"TEST_UNSET": "default",
		"TEST_EMPTY": "default",
		"TEST_SET":   "default",
	})

	for env, want := range map[string]string{
		"TEST_UNSET": "default",
		"TEST_EMPTY": "default",
		"TEST_SET":   "value",
	} {
		if got := os.Getenv(env); got != want {
			t.Errorf("%s = %q, want %q", env, got, want)
		}
	}
}
