//go:build !linux

package testguard

import "golang.org/x/sys/unix"

const procReserve = 1000

func boundProcesses() error {
	capProcesses()
	return nil
}

func capProcesses() {
	var lim unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_NPROC, &lim); err != nil {
		return
	}
	if lim.Cur == unix.RLIM_INFINITY || lim.Cur <= 2*procReserve {
		return
	}
	lim.Cur -= procReserve
	_ = unix.Setrlimit(unix.RLIMIT_NPROC, &lim)
}
