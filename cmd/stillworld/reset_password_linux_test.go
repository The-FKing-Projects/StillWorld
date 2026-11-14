//go:build linux

package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestTerminalPasswordHelper(t *testing.T) {
	if os.Getenv("STILLWORLD_TEST_TERMINAL_HELPER") != "1" {
		return
	}
	_, err := readTerminalPassword(os.Stdin, os.Stdout)
	if err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

func TestInterruptedPasswordPromptRestoresTerminal(t *testing.T) {
	for _, mode := range []string{"signal", "control-c"} {
		t.Run(mode, func(t *testing.T) {
			fd, err := unix.Open("/dev/ptmx", unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
			if err != nil {
				t.Skipf("pseudo-terminal unavailable: %v", err)
			}
			master := os.NewFile(uintptr(fd), "ptmx")
			defer master.Close()
			if err := unix.IoctlSetPointerInt(fd, unix.TIOCSPTLCK, 0); err != nil {
				t.Fatal(err)
			}
			number, err := unix.IoctlGetInt(fd, unix.TIOCGPTN)
			if err != nil {
				t.Fatal(err)
			}
			slave, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", number), os.O_RDWR, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer slave.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestTerminalPasswordHelper$")
			cmd.Env = append(os.Environ(), "STILLWORLD_TEST_TERMINAL_HELPER=1")
			cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			waited := false
			defer func() {
				if !waited {
					_ = cmd.Process.Kill()
					_ = cmd.Wait()
				}
			}()
			prompt := make(chan error, 1)
			go func() {
				_, err := bufio.NewReader(master).ReadString(':')
				prompt <- err
			}()
			select {
			case err := <-prompt:
				if err != nil {
					t.Fatal(err)
				}
			case <-ctx.Done():
				t.Fatal("password prompt timed out")
			}
			if mode == "signal" {
				err = cmd.Process.Signal(os.Interrupt)
			} else {
				_, err = master.Write([]byte{3})
			}
			if err != nil {
				t.Fatal(err)
			}
			err = cmd.Wait()
			waited = true
			if err == nil || ctx.Err() != nil {
				t.Fatalf("interrupted helper returned %v (context: %v)", err, ctx.Err())
			}
			state, err := unix.IoctlGetTermios(fd, unix.TCGETS)
			if err != nil {
				t.Fatal(err)
			}
			if state.Lflag&unix.ECHO == 0 {
				t.Fatal("terminal echo was not restored")
			}
		})
	}
}
