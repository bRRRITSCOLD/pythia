//go:build linux

package sandbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"syscall"
	"testing"

	"github.com/elastic/go-seccomp-bpf/arch"
	"golang.org/x/net/bpf"
	"golang.org/x/sys/unix"
)

// --- Pure, in-process tests on the assembled program -----------------

func nativeArchInfo(t *testing.T) *arch.Info {
	t.Helper()
	info, err := arch.GetInfo("")
	if err != nil {
		t.Skipf("arch.GetInfo: %v (GOARCH=%s not supported by go-seccomp-bpf's arch tables)", err, runtime.GOARCH)
	}
	return info
}

func TestBuildSeccompProgram_DefaultActionIsErrnoENOSYS(t *testing.T) {
	prog, err := buildSeccompProgram(nativeArchInfo(t))
	if err != nil {
		t.Fatalf("buildSeccompProgram: %v", err)
	}
	rc, ok := lastRetConstant(prog)
	if !ok {
		t.Fatalf("last instruction is not a RetConstant: %#v", prog[len(prog)-1])
	}
	if rc != actionErrno(unix.ENOSYS) {
		t.Errorf("default action = 0x%x, want ERRNO(ENOSYS) = 0x%x", rc, actionErrno(unix.ENOSYS))
	}
}

func TestBuildSeccompProgram_IncludesKillForLethalSyscalls(t *testing.T) {
	info := nativeArchInfo(t)
	for _, name := range []string{"mount", "unshare", "bpf", "keyctl"} {
		num, ok := info.SyscallNames[name]
		if !ok {
			t.Skipf("%s not in %s syscall table", name, info.Name)
		}
		prog, err := buildSeccompProgram(info)
		if err != nil {
			t.Fatalf("buildSeccompProgram: %v", err)
		}
		if !hasMatchReturning(prog, uint32(num), actionKillProcess) {
			t.Errorf("no KILL_PROCESS check found for lethal syscall %s (%d)", name, num)
		}
	}
}

func TestBuildSeccompProgram_IncludesDenyForSocketIoUringPtrace(t *testing.T) {
	info := nativeArchInfo(t)
	for _, name := range []string{"socket", "io_uring_setup", "ptrace", "process_vm_readv"} {
		num, ok := info.SyscallNames[name]
		if !ok {
			t.Skipf("%s not in %s syscall table", name, info.Name)
		}
		prog, err := buildSeccompProgram(info)
		if err != nil {
			t.Fatalf("buildSeccompProgram: %v", err)
		}
		if !hasMatchReturning(prog, uint32(num), actionErrno(unix.EACCES)) {
			t.Errorf("no ERRNO(EACCES) check found for denied syscall %s (%d)", name, num)
		}
	}
}

func TestBuildSeccompProgram_AllowsExecveAndCommonSyscalls(t *testing.T) {
	info := nativeArchInfo(t)
	for _, name := range []string{"execve", "read", "write", "openat", "close", "exit_group"} {
		num, ok := info.SyscallNames[name]
		if !ok {
			t.Skipf("%s not in %s syscall table", name, info.Name)
		}
		prog, err := buildSeccompProgram(info)
		if err != nil {
			t.Fatalf("buildSeccompProgram: %v", err)
		}
		if !hasMatchReturning(prog, uint32(num), actionAllow) {
			t.Errorf("no ALLOW check found for %s (%d)", name, num)
		}
	}
}

func TestBuildSeccompProgram_UnsupportedArch_Errors(t *testing.T) {
	// info without a syscall table (execve missing) must fail closed
	// rather than silently building an all-default-deny (but non-functional,
	// since even exec would be denied) filter.
	fake := &arch.Info{Name: "fake", SyscallNames: map[string]int{}}
	if _, err := buildSeccompProgram(fake); err == nil {
		t.Fatal("buildSeccompProgram with no execve mapping: want error, got nil")
	}
}

// --- Helper-subprocess tests: real kernel enforcement -----------------

// seccompHelperEnv gates TestSeccompHelperProcess exactly like
// landlockHelperEnv gates TestLandlockHelperProcess in landlock_linux_test.go:
// a no-op under normal `go test`, real work only when re-invoked as a
// subprocess. A subprocess is required because applySeccomp permanently
// installs a syscall filter for the rest of the calling process's
// lifetime — running it in-process here would leak that filter into every
// later test in this binary.
const seccompHelperEnv = "PYTHIA_SECCOMP_TEST_HELPER"

func TestSeccompHelperProcess(t *testing.T) {
	if os.Getenv(seccompHelperEnv) != "1" {
		return
	}
	os.Exit(seccompHelperMain(os.Args))
}

// runSeccompHelper re-execs this test binary selecting only
// TestSeccompHelperProcess, installs the real seccomp filter inside it via
// seccompHelperMain, and reports back how the subprocess ended: its exit
// code (-1 if signal-killed), whether a signal killed it, and captured
// output.
func runSeccompHelper(t *testing.T, action string, args ...string) (exitCode int, killSignal syscall.Signal, stdout, stderr string) {
	t.Helper()

	cmdArgs := append([]string{"-test.run=^TestSeccompHelperProcess$", "--", action}, args...)
	cmd := exec.Command(os.Args[0], cmdArgs...)
	cmd.Env = append(os.Environ(), seccompHelperEnv+"=1")

	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf

	err := cmd.Run()
	if err == nil {
		return exitOK, 0, outBuf.String(), errBuf.String()
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		if ws, ok := exitErr.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			return -1, ws.Signal(), outBuf.String(), errBuf.String()
		}
		return exitErr.ExitCode(), 0, outBuf.String(), errBuf.String()
	}
	t.Fatalf("run seccomp helper: %v", err)
	return -1, 0, "", ""
}

// seccompHelperMain installs the real filter (failing closed via
// exitApplyErr if that itself fails) and then dispatches to the action
// named after the "--" terminator in args, reusing the exit-code
// vocabulary landlock_linux_test.go already defines in this package
// (exitOK/exitUsageErr/exitUnexpected/exitApplyErr/exitDenied).
func seccompHelperMain(args []string) int {
	action := trimAfterDoubleDash(args)
	if len(action) < 1 {
		fmt.Fprintln(os.Stderr, "seccomp helper: missing action")
		return exitUsageErr
	}

	// seccomp(2) refuses SECCOMP_SET_MODE_FILTER with EACCES unless the
	// calling thread already has NO_NEW_PRIVS set (or CAP_SYS_ADMIN) — the
	// same prerequisite the frozen sequence in child_linux.go establishes
	// before ever reaching applySeccomp.
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		fmt.Fprintf(os.Stderr, "set NO_NEW_PRIVS: %v\n", err)
		return exitApplyErr
	}

	if err := applySeccomp(); err != nil {
		fmt.Fprintf(os.Stderr, "applySeccomp: %v\n", err)
		return exitApplyErr
	}

	switch action[0] {
	case "socket":
		if len(action) != 2 {
			return usageErrf("socket <inet|inet6|unix|netlink>")
		}
		return helperSocket(action[1])
	case "connect-tcp":
		return helperConnectTCP()
	case "io_uring":
		return helperRawDenied(unix.SYS_IO_URING_SETUP, 0, 0, 0)
	case "ptrace":
		return helperRawDenied(unix.SYS_PTRACE, uintptr(unix.PTRACE_TRACEME), 0, 0)
	case "process_vm_readv":
		return helperRawDenied6(unix.SYS_PROCESS_VM_READV, 0, 0, 0, 0, 0, 0)
	case "kcmp":
		return helperRawDenied6(unix.SYS_KCMP, 0, 0, 0, 0, 0, 0)
	case "userfaultfd":
		return helperRawDenied(unix.SYS_USERFAULTFD, 0, 0, 0)
	case "unknown-syscall":
		return helperRawDenied(unix.SYS_SEMGET, 0, 0, 0)
	case "mount":
		// Never dereferenced: seccomp intercepts before the kernel's mount
		// handler would read any of these pointer args.
		return helperRawExpectKilled6(unix.SYS_MOUNT, 0, 0, 0, 0, 0, 0)
	case "bpf":
		return helperRawExpectKilled(unix.SYS_BPF, 0, 0, 0)
	case "keyctl":
		return helperRawExpectKilled6(unix.SYS_KEYCTL, 0, 0, 0, 0, 0, 0)
	case "x32-getpid":
		if runtime.GOARCH != "amd64" {
			fmt.Fprintln(os.Stderr, "x32 probe only meaningful on amd64")
			return exitUsageErr
		}
		return helperRawExpectKilled(unix.SYS_GETPID|x32SyscallMask, 0, 0, 0)
	default:
		return usageErrf(fmt.Sprintf("unknown action %q", action[0]))
	}
}

func helperSocket(family string) int {
	domains := map[string]int{
		"inet":    unix.AF_INET,
		"inet6":   unix.AF_INET6,
		"unix":    unix.AF_UNIX,
		"netlink": unix.AF_NETLINK,
	}
	domain, ok := domains[family]
	if !ok {
		return usageErrf(fmt.Sprintf("unknown family %q", family))
	}
	fd, err := unix.Socket(domain, unix.SOCK_STREAM, 0)
	if err == nil {
		_ = unix.Close(fd)
		fmt.Fprintf(os.Stderr, "socket(%s) unexpectedly succeeded\n", family)
		return exitUnexpected
	}
	return classifySeccompErr(err)
}

func helperConnectTCP() int {
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_STREAM, 0)
	if err != nil {
		return classifySeccompErr(err)
	}
	defer unix.Close(fd)
	sa := &unix.SockaddrInet4{Port: 80, Addr: [4]byte{127, 0, 0, 1}}
	if connErr := unix.Connect(fd, sa); connErr != nil {
		return classifySeccompErr(connErr)
	}
	fmt.Fprintln(os.Stderr, "connect unexpectedly succeeded")
	return exitUnexpected
}

// helperRawDenied issues a raw 3-argument syscall expected to return an
// errno (ENOSYS or EACCES depending on which table it's in) rather than
// succeed or kill the process.
func helperRawDenied(nr uintptr, a1, a2, a3 uintptr) int {
	_, _, errno := syscall.Syscall(nr, a1, a2, a3)
	if errno == 0 {
		fmt.Fprintf(os.Stderr, "syscall %d unexpectedly succeeded\n", nr)
		return exitUnexpected
	}
	return classifySeccompErr(errno)
}

func helperRawDenied6(nr uintptr, a1, a2, a3, a4, a5, a6 uintptr) int {
	_, _, errno := syscall.Syscall6(nr, a1, a2, a3, a4, a5, a6)
	if errno == 0 {
		fmt.Fprintf(os.Stderr, "syscall %d unexpectedly succeeded\n", nr)
		return exitUnexpected
	}
	return classifySeccompErr(errno)
}

// helperRawExpectKilled issues a raw syscall that the lethal-set filter
// should kill the whole process for. If control ever returns here, the
// filter failed to kill it — report that as exitUnexpected rather than
// silently exiting cleanly, so the parent test's signal check has a
// meaningful non-signaled failure to report instead of an ambiguous pass.
func helperRawExpectKilled(nr uintptr, a1, a2, a3 uintptr) int {
	syscall.Syscall(nr, a1, a2, a3)
	fmt.Fprintf(os.Stderr, "syscall %d unexpectedly returned instead of killing the process\n", nr)
	return exitUnexpected
}

func helperRawExpectKilled6(nr uintptr, a1, a2, a3, a4, a5, a6 uintptr) int {
	syscall.Syscall6(nr, a1, a2, a3, a4, a5, a6)
	fmt.Fprintf(os.Stderr, "syscall %d unexpectedly returned instead of killing the process\n", nr)
	return exitUnexpected
}

// classifySeccompErr maps a denied syscall's errno onto the shared
// exit-code vocabulary: EACCES/ENOSYS (our filter's two denial actions)
// count as exitDenied; anything else is unexpected.
func classifySeccompErr(err error) int {
	if errors.Is(err, unix.EACCES) || errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EAFNOSUPPORT) {
		return exitDenied
	}
	fmt.Fprintf(os.Stderr, "unexpected errno: %v\n", err)
	return exitUnexpected
}

func TestSeccomp_SocketAllFamilies_Denied(t *testing.T) {
	for _, family := range []string{"inet", "inet6", "unix", "netlink"} {
		t.Run(family, func(t *testing.T) {
			code, sig, _, stderr := runSeccompHelper(t, "socket", family)
			if sig != 0 {
				t.Fatalf("socket(%s): killed by signal %v (want a clean errno deny); stderr=%s", family, sig, stderr)
			}
			if code != exitDenied {
				t.Fatalf("socket(%s): exit=%d, want %d (denied); stderr=%s", family, code, exitDenied, stderr)
			}
		})
	}
}

func TestSeccomp_IoUring_DeniedOrKilled(t *testing.T) {
	code, sig, _, stderr := runSeccompHelper(t, "io_uring")
	if sig != 0 {
		return // killed is an acceptable outcome too
	}
	if code != exitDenied {
		t.Fatalf("io_uring_setup: exit=%d, want %d (denied) or killed; stderr=%s", code, exitDenied, stderr)
	}
}

func TestSeccomp_CurlStyleNetwork_FailsCleanly(t *testing.T) {
	code, sig, _, stderr := runSeccompHelper(t, "connect-tcp")
	if sig != 0 {
		t.Fatalf("connect-tcp: killed by signal %v (want a clean errno deny); stderr=%s", sig, stderr)
	}
	if code != exitDenied {
		t.Fatalf("connect-tcp: exit=%d, want %d (denied); stderr=%s", code, exitDenied, stderr)
	}
}

func TestSeccomp_ForeignArchX32_Killed(t *testing.T) {
	if runtime.GOARCH != "amd64" {
		t.Skipf("x32 probe not meaningful on GOARCH=%s", runtime.GOARCH)
	}
	code, sig, _, stderr := runSeccompHelper(t, "x32-getpid")
	if sig != syscall.SIGSYS {
		t.Fatalf("x32 getpid: signal=%v code=%d, want killed by SIGSYS; stderr=%s", sig, code, stderr)
	}
}

func TestSeccomp_MemoryPoke_Denied(t *testing.T) {
	for _, action := range []string{"ptrace", "process_vm_readv", "kcmp", "userfaultfd"} {
		t.Run(action, func(t *testing.T) {
			code, sig, _, stderr := runSeccompHelper(t, action)
			if sig != 0 {
				t.Fatalf("%s: killed by signal %v (want a clean errno deny); stderr=%s", action, sig, stderr)
			}
			if code != exitDenied {
				t.Fatalf("%s: exit=%d, want %d (denied); stderr=%s", action, code, exitDenied, stderr)
			}
		})
	}
}

func TestSeccomp_LethalSet_Killed(t *testing.T) {
	for _, action := range []string{"mount", "bpf", "keyctl"} {
		t.Run(action, func(t *testing.T) {
			code, sig, _, stderr := runSeccompHelper(t, action)
			if sig != syscall.SIGSYS {
				t.Fatalf("%s: signal=%v code=%d, want killed by SIGSYS; stderr=%s", action, sig, code, stderr)
			}
		})
	}
}

func TestSeccomp_UnknownSyscall_DefaultDeny(t *testing.T) {
	code, sig, _, stderr := runSeccompHelper(t, "unknown-syscall")
	if sig != 0 {
		t.Fatalf("unknown syscall: killed by signal %v (want ERRNO(ENOSYS) default deny); stderr=%s", sig, stderr)
	}
	if code != exitDenied {
		t.Fatalf("unknown syscall: exit=%d, want %d (denied); stderr=%s", code, exitDenied, stderr)
	}
}

// --- Full production-path tests (real spine: Landlock + seccomp + exec) -

func TestSeccomp_NormalCommand_StillRuns(t *testing.T) {
	var out bytes.Buffer
	code, err := Run(context.Background(), Policy{WorkspaceRoot: t.TempDir(), TmpDir: "/tmp"},
		"echo seccomp-ok", &out, io.Discard)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if code != 0 {
		t.Fatalf("exit=%d, want 0; out=%q", code, out.String())
	}
	if got := strings.TrimSpace(out.String()); got != "seccomp-ok" {
		t.Fatalf("stdout = %q, want %q", got, "seccomp-ok")
	}
}

// TestSeccomp_FilterPersistsIntoBash_PostExecStillDenied proves the filter
// installed before execve (SR-3a.9, TSYNC'd) is still enforced against
// /bin/bash itself after the exec — not merely against the pre-exec
// process image — by having the sandboxed bash try (and fail to reach)
// the network via its /dev/tcp pseudo-device, which goes through
// socket()+connect() under the hood.
func TestSeccomp_FilterPersistsIntoBash_PostExecStillDenied(t *testing.T) {
	var out, errOut bytes.Buffer
	code, err := Run(context.Background(), Policy{WorkspaceRoot: t.TempDir(), TmpDir: "/tmp"},
		"exec 3<>/dev/tcp/127.0.0.1/80 2>&1 || echo denied-post-exec", &out, &errOut)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if code != 0 {
		t.Fatalf("exit=%d, want 0 (command itself should complete, just report denial); out=%q err=%q", code, out.String(), errOut.String())
	}
	if !strings.Contains(out.String(), "denied-post-exec") {
		t.Fatalf("bash's post-exec socket connect was not denied: out=%q err=%q", out.String(), errOut.String())
	}
}

// --- small assembly-introspection helpers -----------------------------

// lastRetConstant returns the K value of prog's final instruction if it is
// a bpf.RetConstant, matching go-seccomp-bpf's own Assemble()'s convention
// of always ending a policy with a single unconditional return.
func lastRetConstant(prog []bpf.Instruction) (uint32, bool) {
	if len(prog) == 0 {
		return 0, false
	}
	rc, ok := prog[len(prog)-1].(bpf.RetConstant)
	if !ok {
		return 0, false
	}
	return rc.Val, true
}

// hasMatchReturning scans prog for a "JumpIf syscall==num" immediately
// followed by "RetConstant{Val: action}" — the two-instruction unit
// appendMatchGroup emits for every allow/deny/kill entry.
func hasMatchReturning(prog []bpf.Instruction, num uint32, action uint32) bool {
	for i := 0; i+1 < len(prog); i++ {
		jmp, ok := prog[i].(bpf.JumpIf)
		if !ok || jmp.Cond != bpf.JumpEqual || jmp.Val != num {
			continue
		}
		ret, ok := prog[i+1].(bpf.RetConstant)
		if ok && ret.Val == action {
			return true
		}
	}
	return false
}
