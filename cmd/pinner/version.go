package main

import (
	"fmt"
	"runtime/debug"
	"strings"
)

// commit is set at build time (-ldflags "-X main.commit=<sha>") by CI. A build from a git checkout also
// carries the revision in the build info, which is used when commit is not set.
var commit = ""

// version says which build this is: the commit, whether the tree was modified, and the Go version.
// It is what to paste next to a bug report: "which pinner is this?" is otherwise hard to tell.
func version() string {
	rev, dirty, when := commit, false, ""
	goVersion := "unknown"
	if info, ok := debug.ReadBuildInfo(); ok {
		goVersion = info.GoVersion
		for _, s := range info.Settings {
			switch s.Key {
			case "vcs.revision":
				if rev == "" {
					rev = s.Value
				}
			case "vcs.modified":
				dirty = s.Value == "true"
			case "vcs.time":
				when = s.Value
			}
		}
	}
	if rev == "" {
		rev = "unknown"
	}
	if len(rev) > 12 {
		rev = rev[:12]
	}
	var b strings.Builder
	fmt.Fprintf(&b, "pinner %s", rev)
	if dirty {
		b.WriteString("+modified")
	}
	if when != "" {
		fmt.Fprintf(&b, " (commit time %s)", when)
	}
	fmt.Fprintf(&b, ", %s", goVersion)
	return b.String()
}

func runVersion(args []string) {
	fmt.Println(version())
}
