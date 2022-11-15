package environmentchecks

import (
	"os"
	"fmt"
)

func HandleDefaults(envDefaults map[string]string){
	for env, defaultValue := range envDefaults{
		// Check if env is set
		_, isSet := os.LookupEnv(env)
		if(!isSet){
			os.Setenv(env, defaultValue)
		}
	}
}

func HandleRequired(envRequired []string) error {
	for _, env := range envRequired{
		// Check if env is set
		_, isSet := os.LookupEnv(env)
		if(!isSet){
			return fmt.Errorf("env '%s' required, but not set", env)
		}
	}
	return nil
}
