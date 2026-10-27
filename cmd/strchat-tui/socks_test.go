package main

import (
	"context"
	"encoding/json"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSOCKSFlagRejectsUnsafeArgumentsBeforeConfig(t *testing.T) {
	if os.Getenv("STRCHAT_SOCKS_CLI_HELPER") == "1" {
		flag.CommandLine = flag.NewFlagSet("strchat-tui", flag.ExitOnError)
		var args []string
		if err := json.Unmarshal([]byte(os.Getenv("STRCHAT_SOCKS_TEST_ARGS")), &args); err != nil {
			t.Fatal(err)
		}
		os.Args = append([]string{"strchat-tui"}, args...)
		main()
		os.Exit(0)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"--socks=http://127.0.0.1:9050"}, "SOCKS5"},
		{[]string{"--socks="}, "SOCKS5"},
		{[]string{"-socks", ""}, "SOCKS5"},
		{[]string{"--socks= "}, "SOCKS5"},
		{[]string{"lobby", "--socks=127.0.0.1:9050"}, "Positional"},
		{[]string{"--socks=127.0.0.1:9050", "lobby"}, "Positional"},
		{[]string{"--", "--socks=127.0.0.1:9050"}, "Positional"},
		{[]string{"--plugin-send="}, "plugin-send executable cannot be empty"},
		{[]string{"-plugin-receive", ""}, "plugin-receive executable cannot be empty"},
		{[]string{"--plugin-send= "}, "plugin-send executable cannot be blank"},
		{[]string{"--plugin-receive=" + filepath.Join(t.TempDir(), "missing")}, "plugin-receive executable is unavailable"},
		{[]string{"--plugin-timeout=0s"}, "plugin timeout must"},
		{[]string{"--plugin-timeout=-1s"}, "plugin timeout must"},
		{[]string{"--plugin-timeout=121s"}, "plugin timeout must"},
	} {
		home := t.TempDir()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		t.Cleanup(cancel)
		args, err := json.Marshal(tc.args)
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.CommandContext(ctx, executable, "-test.run=^TestSOCKSFlagRejectsUnsafeArgumentsBeforeConfig$")
		cmd.Env = append(os.Environ(), "STRCHAT_SOCKS_CLI_HELPER=1", "STRCHAT_SOCKS_TEST_ARGS="+string(args),
			"TERM=strchat-invalid-test-terminal", "HOME="+home, "XDG_CONFIG_HOME="+home, "APPDATA="+home)
		output, err := cmd.CombinedOutput()
		if err == nil || !strings.Contains(string(output), tc.want) {
			t.Fatalf("expected %s validation failure for %q, got %v: %s", tc.want, tc.args, err, output)
		}
		if _, err := os.Stat(filepath.Join(home, "strchat-tui")); !os.IsNotExist(err) {
			t.Fatalf("invalid runtime options touched the configuration: %v", err)
		}
	}
}
