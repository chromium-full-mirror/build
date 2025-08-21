// Copyright 2023 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

//go:build linux

package cred

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"

	"go.chromium.org/build/siso/ui"
)

// https://fuchsia.googlesource.com/fuchsia/+/ba3ebe3223ab95245f974d11f1f0c960dbabbf50/build/bazel/templates/template.bazelrc#73
// ENOKEY when missing to run `gcert`. http://shortn/_WS1VNAwslp
const googleCredHelper = "/google/src/head/depot/google3/devtools/blaze/bazel/credhelper/credhelper"

// DefaultCredentialHelper returns default credential helper's path.
func DefaultCredentialHelper() string {
	// workaround for b/360055934
	ch := make(chan string, 3)
	for i := range 3 {
		go func() {
			if fi, err := os.Stat(googleCredHelper); (err == nil && fi.Mode()&0111 != 0) || errors.Is(err, syscall.ENOKEY) {
				// Make sure it's not a laptop. gLaptop should fall back to luci-auth below.
				// See also go/glinux-roles.
				dist, err := os.ReadFile("/etc/lsb-release")
				if err != nil {
					ui.Default.Warningf("WARNING: Failed to read /etc/lsb-release. Assuming this is not a laptop. err: %s", err)
				}
				if !bytes.Contains(dist, []byte("GOOGLE_ROLE=laptop")) {
					ch <- googleCredHelper
					return
				}
			}
			path, err := exec.LookPath("luci-auth")
			if err == nil {
				ch <- path
				return
			}
			path, err = exec.LookPath("gcloud")
			if err == nil {
				ch <- path
				return
			}
		}()
		select {
		case helper := <-ch:
			return helper
		case <-time.After(5 * time.Second):
			if i == 0 {
				ui.Default.Warningf("WARNING: Accessing /google/src takes longer than expected. Retrying for 10 more seconds...\n")
			}
		}
	}
	ui.Default.Errorf(`ERROR: Timeout while accessing /google/src.
Run "diagnose_me" or you would need RPC access: http://go/request-rpc
`)
	return ""
}

func credHelperErr(fname string, err error) error {
	if fname == googleCredHelper && errors.Is(err, syscall.ENOKEY) {
		return fmt.Errorf("need to run `gcert`: %w", syscall.ENOKEY)
	}
	return err
}
