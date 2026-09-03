package main

import (
	"context"
	"fmt"
	"os"
	"runtime/debug"
	"strings"
)

// buildVersion can be set by release builds with -ldflags "-X main.buildVersion=v1.2.3".
var buildVersion string

func main() {
	if err := runCLI(context.Background(), os.Args[1:], os.Stdin, os.Stdout, os.Stderr, ExecRunner{}, "."); err != nil {
		fmt.Fprintf(os.Stderr, "\nError: %v\n", err)
		os.Exit(1)
	}
}

func currentVersion() string {
	if version := strings.TrimSpace(buildVersion); version != "" {
		return version
	}

	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "dev"
	}
	if version := strings.TrimSpace(info.Main.Version); version != "" && version != "(devel)" {
		return version
	}

	var revision string
	modified := false
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			revision = setting.Value
		case "vcs.modified":
			modified = setting.Value == "true"
		}
	}
	if revision == "" {
		return "dev"
	}
	if len(revision) > 12 {
		revision = revision[:12]
	}
	version := "dev+" + revision
	if modified {
		version += ".dirty"
	}
	return version
}
