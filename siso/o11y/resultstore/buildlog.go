// Copyright 2024 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package resultstore

import (
	"go.chromium.org/build/hashigo/digest"

	"go.chromium.org/build/siso/blob"
)

func (u *Uploader) AddBuildLog(msg string) {
	u.buildLogMu.Lock()
	u.buildLog.WriteString(msg)
	u.buildLogMu.Unlock()
}

func (u *Uploader) BuildLogData() blob.Data {
	// The data is uploaded via UploadFiles, which requires HashFS; fall back
	// to SHA-256 when it is not set (the data is then never uploaded).
	fn := digest.SHA256
	if u.HashFS != nil {
		fn = u.HashFS.DigestFunction()
	}
	u.buildLogMu.Lock()
	data := blob.FromBytes(fn, "build.log", u.buildLog.Bytes())
	u.buildLog.Reset()
	u.buildLogMu.Unlock()
	return data
}
