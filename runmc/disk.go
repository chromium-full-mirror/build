// Copyright 2025 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

//go:build linux

package main

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// DiskType describes the performance characteristics of a disk.
// disk performance parameters can be found at
// https://cloud.google.com/compute/docs/disks/performance
type DiskType struct {
	// IOPSBaseline is the baseline IOPS for the disk type.
	IOPSBaseline int
	// IOPSReadPerGiB is the IOPS per GiB for read operations.
	IOPSReadPerGiB float64
	// IOPSWritePerGiB is the IOPS per GiB for write operations.
	IOPSWritePerGiB float64

	// MiBPSBaseline is the baseline MiBPS for the disk type.
	MiBPSBaseline int
	// MiBPSPerGiB is the MiBPS per GiB for the disk type.
	MiBPSPerGiB float64
}

var knownDiskTypes = map[string]DiskType{
	"pd-standard": {
		IOPSBaseline:    3000,
		IOPSReadPerGiB:  0.75,
		IOPSWritePerGiB: 1.5,
		MiBPSBaseline:   140,
		MiBPSPerGiB:     0.12,
	},
	"pd-ssd": {
		IOPSBaseline:    6000,
		IOPSReadPerGiB:  30,
		IOPSWritePerGiB: 30,
		MiBPSBaseline:   240,
		MiBPSPerGiB:     0.48,
	},
}

func deviceName(ctx context.Context) (string, error) {
	out, err := exec.CommandContext(ctx, "df", "--output=source", ".").Output()
	if err != nil {
		return "", err
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) == 0 {
		return "", fmt.Errorf("failed to detect disk device: %q", out)
	}
	return strings.TrimSpace(lines[len(lines)-1]), nil
}

// see https://www.freedesktop.org/software/systemd/man/latest/systemd.resource-control.html
func (d DiskType) Properties(ctx context.Context, sizeGB int) ([]string, error) {
	var p []string
	dev, err := deviceName(ctx)
	if err != nil {
		return nil, err
	}
	p = append(p, fmt.Sprintf("IOReadBandwidthMax=%s %dM", dev, d.MiBPS(sizeGB)))
	p = append(p, fmt.Sprintf("IOWriteBandwidthMax=%s %dM", dev, d.MiBPS(sizeGB)))
	p = append(p, fmt.Sprintf("IOReadIOPSMax=%s %d", dev, d.readIOPS(sizeGB)))
	p = append(p, fmt.Sprintf("IOWriteIOPSMax=%s %d", dev, d.writeIOPS(sizeGB)))
	return p, nil
}

func (d DiskType) MiBPS(sizeGB int) int {
	return d.MiBPSBaseline + int(float64(sizeGB)*d.MiBPSPerGiB)
}

func (d DiskType) readIOPS(sizeGB int) int {
	return d.IOPSBaseline + int(float64(sizeGB)*d.IOPSReadPerGiB)
}

func (d DiskType) writeIOPS(sizeGB int) int {
	return d.IOPSBaseline + int(float64(sizeGB)*d.IOPSWritePerGiB)
}
