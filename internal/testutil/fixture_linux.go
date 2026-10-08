//go:build linux

// Package testutil provides deterministic subprocesses for lifecycle tests.
package testutil

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
)

// FixtureCommand runs the current Go test binary, relying on the caller's
// TestProcessFixture to call RunFixture. Arguments are POSIX shell-quoted.
func FixtureCommand(mode string) (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
	return "exec " + quote(exe) + " -test.run=^TestProcessFixture$ -- lazyrun-fixture " + quote(mode), nil
}

// RunFixture returns in the regular test runner and exits directly in a helper
// subprocess. No inherited environment variables are needed to select fixtures.
func RunFixture() {
	args := os.Args
	if len(args) < 3 || args[len(args)-2] != "lazyrun-fixture" {
		return
	}
	mode := args[len(args)-1]
	switch mode {
	case "output":
		fmt.Fprint(os.Stdout, "stdout\n")
		fmt.Fprint(os.Stderr, "stderr\n")
		fmt.Fprint(os.Stdout, "partial")
	case "env":
		fmt.Fprintf(os.Stdout, "SNAPSHOT=%s OVERRIDE=%s", os.Getenv("SNAPSHOT"), os.Getenv("OVERRIDE"))
	case "stdin":
		b, err := io.ReadAll(os.Stdin)
		fmt.Fprintf(os.Stdout, "stdin=%d err=%v", len(b), err)
	case "service", "ignore":
		if mode == "ignore" {
			signal.Ignore(syscall.SIGTERM)
		}
		ch := make(chan os.Signal, 2)
		if mode == "service" {
			signal.Notify(ch, syscall.SIGTERM, syscall.SIGUSR1)
		} else {
			signal.Notify(ch, syscall.SIGUSR1)
		}
		fmt.Fprintf(os.Stdout, "ready:%d\n", os.Getpid())
		<-ch
		fmt.Fprint(os.Stdout, "final\n")
	case "parent", "orphan":
		ch := make(chan os.Signal, 2)
		signal.Notify(ch, syscall.SIGTERM)
		childMode := "service"
		if mode == "orphan" {
			childMode = "ignore"
		}
		child := exec.Command(os.Args[0], "-test.run=^TestProcessFixture$", "--", "lazyrun-fixture", childMode)
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
		if err := child.Start(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(99)
		}
		fmt.Fprintf(os.Stdout, "child:%d\n", child.Process.Pid)
		if mode == "orphan" {
			os.Exit(7)
		}
		<-ch
		if err := child.Wait(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(98)
		}
		fmt.Fprint(os.Stdout, "parent-final\n")
	case "chain":
		depth, _ := strconv.Atoi(os.Getenv("CHAIN_DEPTH"))
		fmt.Fprintf(os.Stdout, "generation:%d\n", depth)
		if depth < 25 {
			child := exec.Command(os.Args[0], "-test.run=^TestProcessFixture$", "--", "lazyrun-fixture", "chain")
			child.Env = append(os.Environ(), "CHAIN_DEPTH="+strconv.Itoa(depth+1))
			child.Stdout, child.Stderr = os.Stdout, os.Stderr
			if err := child.Start(); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(96)
			}
			os.Exit(0)
		}
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, syscall.SIGTERM)
		fmt.Fprintf(os.Stdout, "ready:%d\n", os.Getpid())
		<-ch
		fmt.Fprint(os.Stdout, "chain-final\n")
	case "volume":
		_, _ = os.Stdout.Write([]byte(strings.Repeat("x", 4*1024*1024)))
		fmt.Fprint(os.Stdout, "END")
	default:
		fmt.Fprintln(os.Stderr, "unknown fixture", mode)
		os.Exit(97)
	}
	os.Exit(0)
}
