//go:build linux

package shell

import (
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"sync"
	"syscall"

	"golang.org/x/sys/unix"
)

// A command that must reach the network only through the egress proxy
// runs in a network namespace of its own, where the only thing that
// exists is loopback. The proxy is reached through a unix socket file
// bind-mounted in, and two re-executions of this binary make that work
// (the same trick as landlockSentinel):
//
//	bridge: first process in the namespace. Listens on 127.0.0.1:<port>,
//	  copies each connection to the proxy's socket, and runs stage two
//	  as its child, passing its exit status on.
//	nounix: the child. Applies the seccomp filter that takes UDP and
//	  unix sockets away (without it the command could connect to any
//	  path-named socket the host exposes, docker.sock say) and execs the
//	  shell. TCP stays, and TCP in this namespace can only reach the bridge.
const (
	bridgeSentinel = "__janetcode_egress_bridge__"
	nounixSentinel = "__janetcode_egress_nounix__"
)

func init() {
	if len(os.Args) < 2 {
		return
	}
	switch os.Args[1] {
	case bridgeSentinel:
		runBridge(os.Args[2:])
	case nounixSentinel:
		runNoUnix(os.Args[2:])
	}
}

func bridgeDie(err error) {
	fmt.Fprintf(os.Stderr, "janetcode sandbox: %v\n", err)
	os.Exit(126)
}

// runBridge: args are socket, port, then the nounix stage's argv.
func runBridge(args []string) {
	if len(args) < 3 {
		bridgeDie(fmt.Errorf("egress bridge: bad arguments"))
	}
	sock := args[0]
	port, err := strconv.Atoi(args[1])
	if err != nil || port <= 0 || port > 65535 {
		bridgeDie(fmt.Errorf("egress bridge: bad port %q", args[1]))
	}
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", args[1]))
	if err != nil {
		bridgeDie(fmt.Errorf("egress bridge: listen: %w", err))
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go bridgeConn(c, sock)
		}
	}()
	child := exec.Command(args[2], args[3:]...)
	child.Stdin, child.Stdout, child.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := child.Start(); err != nil {
		bridgeDie(fmt.Errorf("egress bridge: start command: %w", err))
	}
	sigs := make(chan os.Signal, 4)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	go func() {
		for sg := range sigs {
			_ = child.Process.Signal(sg)
		}
	}()
	err = child.Wait()
	if ee, ok := err.(*exec.ExitError); ok {
		if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			os.Exit(128 + int(ws.Signal()))
		}
		os.Exit(ee.ExitCode())
	}
	if err != nil {
		bridgeDie(err)
	}
	os.Exit(0)
}

func bridgeConn(c net.Conn, sock string) {
	defer c.Close()
	u, err := net.Dial("unix", sock)
	if err != nil {
		return
	}
	defer u.Close()
	var wg sync.WaitGroup
	wg.Add(2)
	pipe := func(dst, src net.Conn) {
		defer wg.Done()
		_, _ = io.Copy(dst, src)
		if cw, ok := dst.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		}
	}
	go pipe(u, c)
	go pipe(c, u)
	wg.Wait()
}

func runNoUnix(argv []string) {
	if len(argv) == 0 {
		bridgeDie(fmt.Errorf("egress sandbox: no command"))
	}
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		bridgeDie(fmt.Errorf("prctl(no_new_privs): %w", err))
	}
	if err := applyNoNetSeccomp(); err != nil {
		bridgeDie(err)
	}
	if err := syscall.Exec(argv[0], argv, os.Environ()); err != nil {
		bridgeDie(fmt.Errorf("exec %s: %w", argv[0], err))
	}
}

// egressBridgeSupported says whether bash can be put in a namespace of
// its own behind a bridge to the proxy: it needs the socket filter, and
// a path to re-execute this binary by.
func egressBridgeSupported() bool {
	if !seccompSupported {
		return false
	}
	_, err := os.Executable()
	return err == nil
}

// bridgeArgv is what bwrap runs inside the namespace: the bridge stage,
// whose child is the nounix stage, whose exec is the shell.
func bridgeArgv(sock string, port int, shell []string) ([]string, error) {
	self, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("egress bridge: cannot re-execute this binary: %w", err)
	}
	argv := []string{self, bridgeSentinel, sock, strconv.Itoa(port), self, nounixSentinel}
	return append(argv, shell...), nil
}
