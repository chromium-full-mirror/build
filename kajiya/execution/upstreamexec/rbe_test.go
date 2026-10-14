// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package upstreamexec_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"

	"go.chromium.org/build/hashigo/digest"
	repb "go.chromium.org/build/remote-apis/build/bazel/remote/execution/v2"

	"go.chromium.org/build/kajiya/blobstore"
	"go.chromium.org/build/kajiya/execution/model"
	"go.chromium.org/build/kajiya/execution/upstreamexec"
)

// TestExecuteOnRBE verifies the proxy executor against a real upstream
// REAPI service, typically Google RBE.
//
// It defaults to Chromium's untrusted RBE instance and a Chromium
// siso-chromium container image. KAJIYA_TEST_RBE_ENDPOINT,
// KAJIYA_TEST_RBE_INSTANCE and KAJIYA_TEST_RBE_CONTAINER_IMAGE override them.
// The test is skipped unless KAJIYA_TEST_RBE=1 explicitly enables it.
//
// Run manually with:
//
//	luci-auth login -scopes "https://www.googleapis.com/auth/cloud-platform https://www.googleapis.com/auth/userinfo.email"
//	KAJIYA_TEST_RBE=1 go test -run TestExecuteOnRBE -v ./execution/upstreamexec/
func TestExecuteOnRBE(t *testing.T) {
	// Run only on request.
	if os.Getenv("KAJIYA_TEST_RBE") != "1" {
		t.Skip("set KAJIYA_TEST_RBE=1 to run this test against a real upstream REAPI service")
	}

	// Read the configuration and pin the container image.
	endpoint := envOr("KAJIYA_TEST_RBE_ENDPOINT", "remotebuildexecution.googleapis.com:443")
	instance := envOr("KAJIYA_TEST_RBE_INSTANCE", "projects/rbe-chromium-untrusted/instances/default_instance")
	tagged := envOr("KAJIYA_TEST_RBE_CONTAINER_IMAGE", "docker://gcr.io/chops-public-images-prod/rbe/siso-chromium/linux:latest")
	ctx := t.Context()
	image, err := resolveContainerImage(ctx, tagged)
	if err != nil {
		t.Fatalf("resolve container image %q: %v", tagged, err)
	}

	// Connect to RBE.
	conn, err := upstreamexec.Dial(endpoint, false)
	if err != nil {
		t.Fatalf("dial RBE: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	// Create a local CAS and the proxy executor.
	cas, err := blobstore.New(ctx, filepath.Join(t.TempDir(), "cas"))
	if err != nil {
		t.Fatalf("create CAS: %v", err)
	}
	executor, err := upstreamexec.New(ctx, conn, cas, instance, []digest.Function{digest.SHA256})
	if err != nil {
		t.Fatalf("create executor: %v", err)
	}

	// Build a trivial RBE-compatible action with the configured container
	// image, and load it. The shell command writes "hello" into
	// out/result.txt.
	actionDigest := putHelloActionWithImage(t, cas, image)
	action, err := model.LoadAction(digest.SHA256, actionDigest, cas)
	if err != nil {
		t.Fatalf("load action: %v", err)
	}

	// Execute it on RBE, and check that the output landed in the local CAS.
	ar, err := executor.Execute(action)
	if err != nil {
		t.Fatalf("Execute against RBE: %v", err)
	}
	if got, want := ar.ExitCode, int32(0); got != want {
		t.Fatalf("exit code = %d, want %d", got, want)
	}
	if got, want := len(ar.OutputFiles), 1; got != want {
		t.Fatalf("got %d output files, want %d: %+v", got, want, ar.OutputFiles)
	}
	if got, want := ar.OutputFiles[0].Path, "out/result.txt"; got != want {
		t.Fatalf("output file path = %q, want %q", got, want)
	}
	outDigest, err := digest.SHA256.FromProto(ar.OutputFiles[0].Digest)
	if err != nil {
		t.Fatalf("parse output digest: %v", err)
	}
	got, err := cas.Get(digest.SHA256, outDigest)
	if err != nil {
		t.Fatalf("read output from local CAS: %v", err)
	}
	if got, want := string(got), "hello\n"; got != want {
		t.Errorf("output content = %q, want %q", got, want)
	}
}

// envOr returns os.Getenv(key) if non-empty, otherwise fallback.
func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// resolveContainerImage rewrites a tag-based "docker://<registry>/<repo>:<tag>"
// reference to its current digest-pinned form
// "docker://<registry>/<repo>@sha256:<hex>" by querying the registry's v2
// manifest endpoint. Already-pinned references and non-docker:// references
// are returned unchanged. RBE rejects actions whose container-image is a tag
// reference, so the test pins the image before submitting an action.
func resolveContainerImage(ctx context.Context, image string) (string, error) {
	const prefix = "docker://"
	if !strings.HasPrefix(image, prefix) {
		return image, nil
	}
	rest := image[len(prefix):]
	if strings.Contains(rest, "@sha256:") {
		return image, nil
	}
	registry, repoAndTag, ok := strings.Cut(rest, "/")
	if !ok {
		return "", errors.New("missing registry component")
	}
	repo, tag, ok := strings.CutLast(repoAndTag, ":")
	if !ok {
		return "", errors.New("missing tag component")
	}

	// Ask the registry for the manifest's digest.
	url := fmt.Sprintf("https://%s/v2/%s/manifests/%s", registry, repo, tag)
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
	if err != nil {
		return "", fmt.Errorf("build manifest request: %w", err)
	}
	req.Header.Set("Accept", strings.Join([]string{
		"application/vnd.docker.distribution.manifest.v2+json",
		"application/vnd.docker.distribution.manifest.list.v2+json",
		"application/vnd.oci.image.manifest.v1+json",
		"application/vnd.oci.image.index.v1+json",
	}, ","))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch manifest: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("manifest HEAD %s: status %s", url, resp.Status)
	}
	d := resp.Header.Get("Docker-Content-Digest")
	if d == "" {
		return "", errors.New("manifest response missing Docker-Content-Digest header")
	}
	return fmt.Sprintf("%s%s/%s@%s", prefix, registry, repo, d), nil
}

// putHelloActionWithImage uploads a REAPI action that runs
// `/bin/sh -c "echo hello > out/result.txt"` with platform.container-image
// pinned to the given image. It returns the action digest.
//
// The Command sets two fields that REAPI deprecates, because RBE needs them:
// it validates Command.platform and rejects the action with "docker container
// must be specified" without it, and it captures only the outputs listed in
// Command.output_files, not those in output_paths.
func putHelloActionWithImage(t *testing.T, cas *blobstore.ContentAddressableStorage, image string) digest.Digest {
	t.Helper()

	putProto := func(m proto.Message) *repb.Digest {
		t.Helper()
		b, err := proto.Marshal(m)
		if err != nil {
			t.Fatalf("marshal proto: %v", err)
		}
		d, err := cas.Put(digest.SHA256, b)
		if err != nil {
			t.Fatalf("put proto: %v", err)
		}
		return d.Proto()
	}

	outDirDigest := putProto(&repb.Directory{})
	rootDigest := putProto(&repb.Directory{
		Directories: []*repb.DirectoryNode{
			{Name: "out", Digest: outDirDigest},
		},
	})
	platform := &repb.Platform{
		Properties: []*repb.Platform_Property{
			{Name: "container-image", Value: image},
		},
	}
	cmdDigest := putProto(&repb.Command{
		Arguments:   []string{"/bin/sh", "-c", "echo hello > out/result.txt"},
		OutputFiles: []string{"out/result.txt"}, //nolint:staticcheck // RBE captures only the outputs listed here.
		OutputPaths: []string{"out/result.txt"},
		Platform:    platform, //nolint:staticcheck // RBE rejects the action without a Command platform.
	})
	actionPb := putProto(&repb.Action{
		CommandDigest:   cmdDigest,
		InputRootDigest: rootDigest,
		Platform:        platform,
	})
	ad, err := digest.SHA256.FromProto(actionPb)
	if err != nil {
		t.Fatalf("parse action digest: %v", err)
	}
	return ad
}
