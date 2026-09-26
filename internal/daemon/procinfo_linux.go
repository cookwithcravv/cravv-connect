//go:build linux

package daemon

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// procParent returns the parent and the process group of process pid,
// from /proc/<pid>/stat.
func procParent(pid int) (ppid, pgid int, err error) {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0, 0, err
	}
	// pid (comm) state ppid pgrp ...; comm may hold spaces and parentheses.
	s := string(b)
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return 0, 0, fmt.Errorf("parse /proc/%d/stat", pid)
	}
	f := strings.Fields(s[i+1:])
	if len(f) < 3 {
		return 0, 0, fmt.Errorf("parse /proc/%d/stat", pid)
	}
	if ppid, err = strconv.Atoi(f[1]); err != nil {
		return 0, 0, err
	}
	if pgid, err = strconv.Atoi(f[2]); err != nil {
		return 0, 0, err
	}
	return ppid, pgid, nil
}

// bootID names this boot of the machine: a process group recorded in
// another boot is never killed.
func bootID() string {
	b, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}
