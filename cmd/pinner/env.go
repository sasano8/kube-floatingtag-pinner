package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
)

// envPrefix starts the name of the environment variable of every flag: --log-level is PINNER_LOG_LEVEL.
const envPrefix = "PINNER_"

// envName is the environment variable that sets a flag.
func envName(flagName string) string {
	return envPrefix + strings.ToUpper(strings.ReplaceAll(flagName, "-", "_"))
}

// flagsFromEnv names, for the flags that took their value from the environment, the variable it came from.
var flagsFromEnv = map[string]string{}

// parseFlags parses the command line, and then gives every flag that was not on it the value of its
// environment variable, if that is set. The command line wins over the environment, and the environment
// over the default. A variable that is set but empty sets an empty value.
func parseFlags(fs *flag.FlagSet, args []string) {
	fs.Parse(args)
	if err := applyEnv(fs, os.LookupEnv); err != nil {
		fatal(err)
	}
}

func applyEnv(fs *flag.FlagSet, lookupEnv func(string) (string, bool)) error {
	onCommandLine := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { onCommandLine[f.Name] = true })
	var err error
	fs.VisitAll(func(f *flag.Flag) {
		if err != nil || onCommandLine[f.Name] {
			return
		}
		name := envName(f.Name)
		v, ok := lookupEnv(name)
		if !ok {
			return
		}
		if e := fs.Set(f.Name, v); e != nil {
			err = fmt.Errorf("%s=%q: %w", name, v, e)
			return
		}
		flagsFromEnv[f.Name] = name
	})
	return err
}
