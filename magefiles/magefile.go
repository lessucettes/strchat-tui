//go:build mage

package main

import (
	"fmt"
	"runtime"
	"time"

	"github.com/magefile/mage/mg"
	"github.com/magefile/mage/sh"
)

const (
	binBase = "strchat-tui"
	pkgPath = "./cmd/strchat-tui"
)

var (
	goexe   = "go"
	version = "dev"
	commit  = "local"
	date    string
	ldFlags string
)

func init() {
	if runtime.GOOS == "windows" {
		goexe = "go.exe"
	}

	if runtime.GOOS != "windows" {
		if v, err := sh.Output("git", "describe", "--tags", "--abbrev=0"); err == nil && v != "" {
			version = v
		}
		if c, err := sh.Output("git", "rev-parse", "--short", "HEAD"); err == nil && c != "" {
			commit = c
		}
	}

	date = time.Now().UTC().Format(time.RFC3339)
	ldFlags = fmt.Sprintf("-s -w -X main.version=%s -X main.commit=%s -X main.date=%s",
		version, commit, date)
}

var Default = All

// Build binaries for supported platforms
func All() {
	mg.Deps(
		func() error { return build("linux", "amd64", binBase+"-linux") },
		func() error { return build("windows", "amd64", binBase+".exe") },
		func() error { return build("darwin", "amd64", binBase+"-macos-intel") },
		func() error { return build("darwin", "arm64", binBase+"-macos-arm") },
	)
}

// Build for Linux/amd64
func Linux() error {
	return build("linux", "amd64", binBase)
}

// Build for Windows/amd64
func Windows() error {
	return build("windows", "amd64", binBase+".exe")
}

// Build for macOS/amd64
func MacIntel() error {
	return build("darwin", "amd64", binBase)
}

// Build for macOS/arm64
func MacARM() error {
	return build("darwin", "arm64", binBase)
}

// Build for macOS
func MacOS() {
	mg.Deps(
		func() error {
			return build("darwin", "arm64", binBase+"-macos-arm")
		},
		func() error {
			return build("darwin", "amd64", binBase+"-macos-intel")
		},
	)
}

func build(goos, goarch string, out string) error {
	env := map[string]string{
		"GOOS":        goos,
		"GOARCH":      goarch,
		"CGO_ENABLED": "0",
	}
	fmt.Printf("Building %s/%s → %s\n", goos, goarch, out)
	return sh.RunWithV(env, goexe, "build", "-ldflags", ldFlags, "-o", out, pkgPath)
}
