package main

import (
	"flag"
	"strings"
	"testing"
	"time"
)

func testFlags() (*flag.FlagSet, *string, *bool, *time.Duration) {
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	level := fs.String("log-level", "info", "")
	debug := fs.Bool("debug", false, "")
	interval := fs.Duration("interval", 5*time.Minute, "")
	return fs, level, debug, interval
}

func envOf(m map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) { v, ok := m[k]; return v, ok }
}

func TestEnvName(t *testing.T) {
	for in, want := range map[string]string{"debug": "PINNER_DEBUG", "log-level": "PINNER_LOG_LEVEL", "pull-namespace": "PINNER_PULL_NAMESPACE"} {
		if got := envName(in); got != want {
			t.Errorf("envName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestApplyEnv(t *testing.T) {
	flagsFromEnv = map[string]string{}

	// the environment fills what the command line did not set
	fs, level, debug, interval := testFlags()
	fs.Parse([]string{"--interval=1m"})
	err := applyEnv(fs, envOf(map[string]string{
		"PINNER_LOG_LEVEL": "warn",
		"PINNER_DEBUG":     "true",
		"PINNER_INTERVAL":  "10m", // the command line wins
	}))
	if err != nil {
		t.Fatal(err)
	}
	if *level != "warn" || !*debug || *interval != time.Minute {
		t.Errorf("level=%q debug=%v interval=%v", *level, *debug, *interval)
	}
	if flagsFromEnv["log-level"] != "PINNER_LOG_LEVEL" || flagsFromEnv["interval"] != "" {
		t.Errorf("flagsFromEnv = %v", flagsFromEnv)
	}

	// nothing set: the defaults stay
	fs, level, debug, _ = testFlags()
	fs.Parse(nil)
	if err := applyEnv(fs, envOf(nil)); err != nil {
		t.Fatal(err)
	}
	if *level != "info" || *debug {
		t.Errorf("defaults changed: level=%q debug=%v", *level, *debug)
	}

	// a value that does not parse is an error that names the variable
	fs, _, _, _ = testFlags()
	fs.Parse(nil)
	err = applyEnv(fs, envOf(map[string]string{"PINNER_INTERVAL": "soon"}))
	if err == nil || !strings.Contains(err.Error(), "PINNER_INTERVAL") {
		t.Errorf("err = %v", err)
	}
}
