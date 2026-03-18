// Copyright 2025 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

//go:build linux

/*
Binary runmc is a tool to run command in machine resource mimicked container.

Usage:

	$ runmc [options] cmdline...
*/
package main

import (
	"context"
	"flag"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

type multiFlag struct {
	v []string
}

func (p *multiFlag) String() string {
	return strings.Join(p.v, ",")
}

func (p *multiFlag) Set(v string) error {
	p.v = append(p.v, v)
	return nil
}

var (
	machineTypeFlag         = flag.String("machine_type", "", fmt.Sprintf("machine type (for cpus, memory). %q", slices.Sorted(maps.Keys(knownMachineTypes))))
	systemReservedMemGBFlag = flag.Int("system_reserved_mem_gb", 1, "system reserved `memory_size` in GB")
	swapGBFlag              = flag.Int("swap_gb", 1, "swap `space` in GB")
	diskTypeFlag            = flag.String("disk_type", "", fmt.Sprintf("disk type. %q", slices.Sorted(maps.Keys(knownDiskTypes))))
	diskSizeGBFlag          = flag.Int("disk_size_gb", 0, "`disk_size` in GB")
	properties              multiFlag
	setenvs                 multiFlag
)

func main() {
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), `%s: command in machine resource mimicked container

Usage: %s [flags] <command line>
`,
			os.Args[0],
			os.Args[0])
		flag.PrintDefaults()
	}
	flag.Var(&properties, "p", "additional properties. see https://www.freedesktop.org/software/systemd/man/latest/systemd.resource-control.html")
	flag.Var(&setenvs, "E", "set environment variables to process. NAME, or NAME=VALUE")
	flag.Parse()
	err := run(context.Background())
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	os.Exit(0)
}

func run(ctx context.Context) error {
	err := checkCgroupSettings(ctx)
	if err != nil {
		return err
	}

	args := []string{
		"systemd-run",
		"--same-dir",
		"--collect",
		"--wait",
		"--pty",
		"--user",
		"--json=pretty",
		"-p", "MemoryAccounting=yes",
		"-p", "IOAccounting=yes",
		"-p", "IPAccounting=yes",
	}
	m, ok := knownMachineTypes[*machineTypeFlag]
	if ok {
		fmt.Printf("mimic machine_type: %q (cpu=%d mem=%dGB)\n", *machineTypeFlag, m.CPUs, m.MemGB)
		if *diskTypeFlag == "" {
			*diskTypeFlag = m.DefaultDiskType
		}
		if *diskSizeGBFlag == 0 {
			*diskSizeGBFlag = m.DefaultDiskSizeGB
		}
		props, err := m.Properties(ctx, *systemReservedMemGBFlag, *swapGBFlag)
		if err != nil {
			return err
		}
		for _, p := range props {
			args = append(args, "-p", p)
		}
		d, ok := knownDiskTypes[*diskTypeFlag]
		if !ok {
			return fmt.Errorf("unknown disk_type=%q, known=%q", *diskTypeFlag, slices.Sorted(maps.Keys(knownDiskTypes)))
		}
		fmt.Printf("mimic disk:         %q %dGB\n", *diskTypeFlag, *diskSizeGBFlag)
		props, err = d.Properties(ctx, *diskSizeGBFlag)
		if err != nil {
			return err
		}
		for _, p := range props {
			args = append(args, "-p", p)
		}
	}
	for _, p := range properties.v {
		args = append(args, "-p", p)
	}
	for _, e := range setenvs.v {
		args = append(args, "-E", e)
	}
	fmt.Printf("running: %q\n", args)
	cmdline := flag.Args()
	if len(cmdline) == 0 {
		cmdline = []string{os.Getenv("SHELL")}
	}
	args = append(args, cmdline...)
	netStart, nerr := netStatistics()
	if nerr != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", nerr)
	}
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	err = cmd.Run()
	netEnd, nerr := netStatistics()
	if nerr != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", nerr)
	}
	netDiff := netStatisticsDiff(netEnd, netStart)
	fmt.Printf("system-wide network activities:\n")
	for _, k := range slices.Sorted(maps.Keys(netDiff)) {
		fmt.Printf("%8s: %8.2f MiB (err:%d) down, %8.2f MiB (err:%d) up\n",
			k,
			float64(netDiff[k].rx.bytes)/1024/1024,
			netDiff[k].rx.errors,
			float64(netDiff[k].tx.bytes)/1024/1024,
			netDiff[k].tx.errors)
	}
	return err
}

type netStats struct {
	bytes   int64
	errors  int64
	packets int64
}

type nicStats struct {
	rx, tx netStats
}

func netStatisticsDiff(end, start map[string]nicStats) map[string]nicStats {
	diff := make(map[string]nicStats)
	for k := range end {
		diff[k] = nicStatsDiff(end[k], start[k])
	}
	return diff
}

func nicStatsDiff(end, start nicStats) nicStats {
	var diff nicStats
	diff.rx.bytes = end.rx.bytes - start.rx.bytes
	diff.rx.errors = end.rx.errors - start.rx.errors
	diff.rx.packets = end.rx.packets - start.rx.packets
	diff.tx.bytes = end.tx.bytes - start.tx.bytes
	diff.tx.errors = end.tx.errors - start.tx.errors
	diff.tx.packets = end.tx.packets - start.tx.packets
	return diff
}

func netStatistics() (map[string]nicStats, error) {
	dirs, err := os.ReadDir("/sys/class/net")
	if err != nil {
		return nil, err
	}
	nics := make(map[string]nicStats)
	for _, dir := range dirs {
		var ns nicStats
		ns, err = nicStatsValue(filepath.Join("/sys/class/net", dir.Name()))
		if err != nil {
			return nil, err
		}
		nics[dir.Name()] = ns
	}
	return nics, nil
}

func nicStatsValue(dirname string) (nicStats, error) {
	var ns nicStats
	var err error
	ns.rx, err = netStatsValue(dirname, "rx")
	if err != nil {
		return ns, err
	}
	ns.tx, err = netStatsValue(dirname, "tx")
	if err != nil {
		return ns, err
	}
	return ns, nil
}

func netStatsValue(dirname, direction string) (netStats, error) {
	var ns netStats
	var err error
	ns.bytes, err = netStatsData(filepath.Join(dirname, "statistics", direction+"_bytes"))
	if err != nil {
		return ns, err
	}
	ns.errors, err = netStatsData(filepath.Join(dirname, "statistics", direction+"_errors"))
	if err != nil {
		return ns, err
	}
	ns.packets, err = netStatsData(filepath.Join(dirname, "statistics", direction+"_packets"))
	if err != nil {
		return ns, err
	}
	return ns, nil
}

func netStatsData(fname string) (int64, error) {
	buf, err := os.ReadFile(fname)
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(strings.TrimSpace(string(buf)), 10, 64)
}

// Check that all required cgroups were delegated to us.
func checkCgroupSettings(ctx context.Context) error {
	fname := fmt.Sprintf("/sys/fs/cgroup/user.slice/user-%d.slice/cgroup.controllers", os.Getuid())
	buf, err := os.ReadFile(fname)
	if err != nil {
		return err
	}
	delegatedCgroups := strings.TrimSpace(string(buf))
	wantDelegatedCgroups := "cpuset cpu io memory pids"
	if delegatedCgroups != wantDelegatedCgroups {
		return fmt.Errorf(`not all required cgroup were delegated: %q; want %q
Please setup cgroup delegation and reboot your machine:
 $ sudo tee /etc/systemd/system/user@.service.d/delegate.conf <<EOF
 [Service]
 Delegate=yes
 EOF`, delegatedCgroups, wantDelegatedCgroups)
	}
	return nil
}
