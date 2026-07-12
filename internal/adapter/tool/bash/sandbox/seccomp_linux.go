//go:build linux

package sandbox

import (
	"fmt"
	"syscall"
	"unsafe"

	"github.com/elastic/go-seccomp-bpf/arch"
	"golang.org/x/net/bpf"
	"golang.org/x/sys/unix"
)

// Offsets into the kernel's seccomp_data struct that the BPF program reads
// via LoadAbsolute — see linux/seccomp.h. nr is the syscall number, arch is
// the AUDIT_ARCH_* constant identifying the calling convention the kernel
// decoded the syscall under.
const (
	seccompDataNROffset   = 0
	seccompDataArchOffset = 4
)

// x32SyscallMask is the bit the x86-64 kernel ORs into the syscall number
// when a process makes a syscall via the x32 ABI (32-bit userland calling
// convention, 64-bit registers) rather than native x86-64. Both ABIs report
// AUDIT_ARCH_X86_64, so the syscall number's high bit is the only signal
// that distinguishes them (threat model §3.1, SR-3a.4).
const x32SyscallMask = 0x40000000

// Seccomp actions, expressed as the raw SECCOMP_RET_* values seccomp(2)
// expects loaded into the BPF accumulator on return. actionErrno(errno)
// embeds the errno to report in the low 16 bits, exactly as the kernel ABI
// requires (SECCOMP_RET_ERRNO | (errno & SECCOMP_RET_DATA)) — this is why
// applySeccomp assembles the filter itself with golang.org/x/net/bpf
// rather than going through go-seccomp-bpf's Policy/Filter/LoadFilter: that
// higher-level API hardcodes EPERM for every ActionErrno group and
// restricts Policy.DefaultAction to a fixed enum that cannot carry a
// specific errno, which cannot express this policy's frozen action table
// (allow / errno(ENOSYS) default / errno(EACCES) deny / kill_process).
const (
	actionAllow       = uint32(unix.SECCOMP_RET_ALLOW)
	actionKillProcess = uint32(unix.SECCOMP_RET_KILL_PROCESS)
	actionErrnoBase   = uint32(unix.SECCOMP_RET_ERRNO)
)

func actionErrno(errno unix.Errno) uint32 {
	return actionErrnoBase | (uint32(errno) & 0xffff)
}

// lethalSyscalls is killed outright (SECCOMP_RET_KILL_PROCESS) rather than
// merely denied: each one is a namespace/mount/module/privilege-boundary
// primitive with no legitimate use inside a confined shell command, and a
// program that reaches for one is behaving adversarially rather than
// merely probing an unavailable feature (SR-3a.14, threat model §3.1).
var lethalSyscalls = []string{
	"mount", "umount", "umount2", "pivot_root", "chroot",
	"reboot", "kexec_load", "kexec_file_load",
	"init_module", "finit_module", "delete_module",
	"unshare", "setns",
	"swapon", "swapoff",
	"bpf", "perf_event_open",
	"add_key", "request_key", "keyctl",
	"acct", "quotactl", "syslog",
	"open_by_handle_at", "name_to_handle_at",
	"iopl", "ioperm", "modify_ldt",
}

// denySyscalls is refused with a clean errno (EACCES) rather than killed:
// the socket family (every address family, not just network — including
// AF_UNIX/docker.sock and abstract sockets), io_uring (a syscall-filter
// bypass vector: its submission-queue protocol lets a program issue
// further syscalls the BPF filter never sees), and the ptrace/memory-poke
// family that would otherwise let a confined command inspect or rewrite
// another process's memory. Denying cleanly (rather than killing) lets a
// program that merely probes for an optional facility fail over instead of
// taking the whole sandboxed command down with it (SR-3a.2, SR-3a.3,
// SR-3a.5).
var denySyscalls = []string{
	"socket", "socketpair",
	"connect", "accept", "accept4", "bind", "listen",
	"sendto", "recvfrom", "sendmsg", "recvmsg", "sendmmsg", "recvmmsg",
	"shutdown", "getsockopt", "setsockopt", "getsockname", "getpeername",
	"io_uring_setup", "io_uring_enter", "io_uring_register",
	"ptrace", "process_vm_readv", "process_vm_writev", "kcmp", "userfaultfd",
}

// allowSyscalls is the default-deny allowlist: only these may execute
// (SECCOMP_RET_ALLOW), covering what /bin/bash and ordinary coreutils need
// for process control, file I/O, and timing. Everything not listed here
// (and not in lethalSyscalls/denySyscalls) falls through to the filter's
// final default action, ERRNO(ENOSYS) (SR-3a.1).
var allowSyscalls = []string{
	// Process lifecycle and signals.
	"execve", "execveat",
	"exit", "exit_group",
	"wait4", "waitid",
	"clone", "clone3", "fork", "vfork",
	"kill", "tkill", "tgkill",
	"rt_sigaction", "rt_sigprocmask", "rt_sigreturn", "rt_sigsuspend",
	"rt_sigtimedwait", "rt_sigpending", "rt_sigqueueinfo", "sigaltstack",
	"getpid", "gettid", "getppid", "getpgrp", "getpgid", "setpgid",
	"getsid", "setsid",
	"prctl", "arch_prctl", "personality",

	// Memory management.
	"mmap", "munmap", "mprotect", "mremap", "brk", "madvise", "msync",
	"membarrier",

	// File descriptor I/O.
	"read", "write", "readv", "writev",
	"pread64", "pwrite64", "preadv", "pwritev", "preadv2", "pwritev2",
	"open", "openat", "openat2", "close", "close_range", "lseek",
	"dup", "dup2", "dup3", "fcntl", "ioctl",

	// Filesystem metadata and namespace operations (Landlock, applied
	// before this filter in the frozen sequence, governs which paths these
	// may actually touch — this list only says the syscalls themselves may
	// execute).
	"stat", "fstat", "lstat", "statx", "newfstatat",
	"access", "faccessat", "faccessat2",
	"readlink", "readlinkat", "getcwd", "chdir", "fchdir",
	"mkdir", "mkdirat", "rmdir", "unlink", "unlinkat",
	"rename", "renameat", "renameat2",
	"link", "linkat", "symlink", "symlinkat",
	"chmod", "fchmod", "fchmodat", "chown", "fchown", "fchownat", "lchown",
	"truncate", "ftruncate", "utime", "utimes", "utimensat", "futimesat",
	"getdents", "getdents64",
	"flock", "fsync", "fdatasync", "fallocate", "sync", "syncfs",
	"statfs", "fstatfs", "umask",

	// Pipes, polling, event notification.
	"pipe", "pipe2", "poll", "ppoll", "select", "pselect6",
	"epoll_create", "epoll_create1", "epoll_ctl", "epoll_wait", "epoll_pwait",
	"epoll_pwait2",
	"eventfd", "eventfd2", "signalfd", "signalfd4",
	"timerfd_create", "timerfd_settime", "timerfd_gettime",
	"splice", "tee", "vmsplice", "copy_file_range", "sendfile",
	"getrandom", "ioprio_get", "ioprio_set",

	// Time.
	"clock_gettime", "clock_getres", "clock_nanosleep", "nanosleep",
	"gettimeofday", "time", "times",

	// Identity (read-only + same-privilege set/reset — NO_NEW_PRIVS,
	// already set earlier in the frozen sequence, ensures none of these
	// can gain privilege).
	"getuid", "geteuid", "getgid", "getegid",
	"getresuid", "getresgid", "getgroups",
	"setuid", "setgid", "setgroups", "setresuid", "setresgid",
	"setfsuid", "setfsgid", "setregid", "setreuid",

	// Resource limits and scheduling.
	"getrlimit", "setrlimit", "prlimit64", "getrusage", "sysinfo",
	"getpriority", "setpriority",
	"sched_getaffinity", "sched_setaffinity", "sched_yield",
	"sched_getparam", "sched_getscheduler", "sched_setscheduler",

	// Misc runtime support.
	"uname",
	"set_tid_address", "set_robust_list", "get_robust_list",
	"rseq", "futex", "futex_waitv", "restart_syscall",
}

// buildSeccompProgram assembles the BPF program for info's architecture:
// kill on any syscall made under a foreign architecture or (on x86_64) the
// x32 ABI, then kill/deny/allow by syscall name per the tables above, then
// ERRNO(ENOSYS) as the final default for anything unmatched. Actions are
// evaluated in that fixed order — lethal, then deny, then allow — so a
// syscall never accidentally ends up in more than one table with
// conflicting effect.
func buildSeccompProgram(info *arch.Info) ([]bpf.Instruction, error) {
	if _, ok := info.SyscallNames["execve"]; !ok {
		return nil, fmt.Errorf("sandbox: arch %s has no execve mapping, cannot build a filter that can still exec", info.Name)
	}

	prog := make([]bpf.Instruction, 0, 4*(len(lethalSyscalls)+len(denySyscalls)+len(allowSyscalls))+8)

	// Foreign architecture: kill. A syscall entered under any arch other
	// than this process's own native one is exactly the confused-deputy
	// shape a seccomp bypass relies on (SR-3a.4).
	prog = append(prog,
		bpf.LoadAbsolute{Off: seccompDataArchOffset, Size: 4},
		bpf.JumpIf{Cond: bpf.JumpEqual, Val: uint32(info.ID), SkipTrue: 1, SkipFalse: 0},
		bpf.RetConstant{Val: actionKillProcess},
	)

	prog = append(prog, bpf.LoadAbsolute{Off: seccompDataNROffset, Size: 4})

	// x32 ABI on x86_64: kill. Distinct check from the general
	// foreign-arch guard above because x32 shares AUDIT_ARCH_X86_64 with
	// native 64-bit — only the syscall number's high bit tells them apart.
	if info == arch.X86_64 {
		prog = append(prog,
			bpf.JumpIf{Cond: bpf.JumpGreaterOrEqual, Val: x32SyscallMask, SkipTrue: 0, SkipFalse: 1},
			bpf.RetConstant{Val: actionKillProcess},
		)
	}

	prog = appendMatchGroup(prog, info, lethalSyscalls, actionKillProcess)
	prog = appendMatchGroup(prog, info, denySyscalls, actionErrno(unix.EACCES))
	prog = appendMatchGroup(prog, info, allowSyscalls, actionAllow)

	// Default: anything not explicitly named above.
	prog = append(prog, bpf.RetConstant{Val: actionErrno(unix.ENOSYS)})

	return prog, nil
}

// appendMatchGroup appends one "syscall number == N -> return action" check
// per name in names that info's syscall table actually has an entry for
// (silently skipping names that don't exist on this architecture, e.g.
// 32-bit-only legacy calls absent from arm64's table — the syscall simply
// isn't reachable there, so there is nothing to allow or deny). Each check
// is a self-contained two-instruction unit (compare, then a same-position
// return) with fixed skip distances of 0/1, so the list's total length
// never risks exceeding BPF's 8-bit jump-offset range regardless of how
// many syscalls are in play.
func appendMatchGroup(prog []bpf.Instruction, info *arch.Info, names []string, action uint32) []bpf.Instruction {
	for _, name := range names {
		num, ok := info.SyscallNames[name]
		if !ok {
			continue
		}
		prog = append(prog,
			bpf.JumpIf{Cond: bpf.JumpEqual, Val: uint32(num), SkipTrue: 0, SkipFalse: 1},
			bpf.RetConstant{Val: action},
		)
	}
	return prog
}

// loadSeccompFilter assembles prog and installs it via the seccomp(2)
// syscall with SECCOMP_FILTER_FLAG_TSYNC, so the filter applies to every
// thread of the calling process (not just the one that installs it) as
// soon as this call returns — the frozen sequence in child_linux.go
// installs it as the very last step before execve, on the single
// OS-thread-locked goroutine that then performs that execve (SR-3a.9).
func loadSeccompFilter(prog []bpf.Instruction) error {
	raw, err := bpf.Assemble(prog)
	if err != nil {
		return fmt.Errorf("sandbox: assemble seccomp BPF program: %w", err)
	}

	filter := make([]unix.SockFilter, len(raw))
	for i, instr := range raw {
		filter[i] = unix.SockFilter{Code: instr.Op, Jt: instr.Jt, Jf: instr.Jf, K: instr.K}
	}
	fprog := unix.SockFprog{
		Len:    uint16(len(filter)),
		Filter: &filter[0],
	}

	_, _, errno := syscall.Syscall(unix.SYS_SECCOMP,
		uintptr(unix.SECCOMP_SET_MODE_FILTER),
		uintptr(unix.SECCOMP_FILTER_FLAG_TSYNC),
		uintptr(unsafe.Pointer(&fprog)),
	)
	if errno != 0 {
		return fmt.Errorf("sandbox: install seccomp filter: %w", errno)
	}
	return nil
}

// applySeccomp installs the sandbox's default-deny seccomp-bpf syscall
// filter on the calling (locked) OS thread, TSYNC'd across the whole
// process. It is the last control installed in the frozen apply sequence
// (child_linux.go), immediately before execve into /bin/bash — the
// allowlist includes execve/execveat precisely so that final exec is
// itself permitted.
func applySeccomp() error {
	info, err := arch.GetInfo("")
	if err != nil {
		return fmt.Errorf("sandbox: determine native arch: %w", err)
	}

	prog, err := buildSeccompProgram(info)
	if err != nil {
		return err
	}

	return loadSeccompFilter(prog)
}
