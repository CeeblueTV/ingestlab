package main

import "runtime/debug"

// version is stamped by release builds: -ldflags "-X main.version=v1.2.3".
var version string

// buildVersion falls back on what the toolchain recorded: the module version under
// go install, a pseudo-version from git under a local build.
func buildVersion() string {
	if version != "" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return "devel"
}
