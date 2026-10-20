package environmentchecks

import (
	"os"
)

// HandleDefaults sets every env that is unset or empty to its default.
func HandleDefaults(envDefaults map[string]string) {
	for env, defaultValue := range envDefaults {
		if os.Getenv(env) == "" {
			os.Setenv(env, defaultValue)
		}
	}
}
