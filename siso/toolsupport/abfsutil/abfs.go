// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package abfsutil

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"syscall"

	log "github.com/golang/glog"

	"go.chromium.org/build/hashigo/digest"

	"go.chromium.org/build/siso/o11y/clog"
	"go.chromium.org/build/siso/reapi/merkletree"
)

// Client is abfs client.
type Client struct {
	endpoint string
	dir      string
	client   *http.Client
}

// New creates new abfs client mounted at dir.
func New(ctx context.Context, endpoint, dir string) (*Client, error) {
	tr := &http.Transport{
		// no proxy
		Proxy: func(*http.Request) (*url.URL, error) {
			return nil, nil
		},
	}
	endpoint = strings.TrimSuffix(endpoint, "/")
	sockPath, ok := strings.CutPrefix(endpoint, "unix://")
	if ok {
		if !filepath.IsAbs(sockPath) {
			sockPath = filepath.Join(dir, sockPath)
		}
		tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", sockPath)
		}
		// go's http requires http or https scheme.
		endpoint = "http://unix"
	}
	return &Client{
		endpoint: endpoint,
		dir:      dir,
		client: &http.Client{
			Transport: tr,
		},
	}, nil
}

// Close closes client to abfs server.
func (c *Client) Close() error {
	return nil
}

// Registration is a registration for a file.
type Registration struct {
	Entry merkletree.Entry

	// result of RegisterFiles
	Err error
}

// RBEPathStat is json object used with ABFS.
type RBEPathStat struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
	Mode   uint32 `json:"mode,omitempty"`
}

// SetRBEDigestsReq is json object of set-rbe-digests request.
type SetRBEDigestsReq struct {
	Digests []RBEPathStat `json:"digests"`
}

// Digest returns digest of the file.
// file is absolute path.
func (c *Client) Digest(ctx context.Context, fname string) (digest.Digest, error) {
	if c == nil || c.endpoint == "" || c.dir == "" || c.client == nil {
		return digest.Digest{}, errors.ErrUnsupported
	}
	relpath, err := filepath.Rel(c.dir, fname)
	if err != nil {
		return digest.Digest{}, fmt.Errorf("abfs: get digest: out of dir: %q", fname)
	}
	if !filepath.IsLocal(relpath) {
		return digest.Digest{}, fmt.Errorf("abfs: get digest: out of dir: %q", fname)
	}
	relpath = filepath.ToSlash(relpath)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint+"/mnt/get-rbe-digest?path="+url.QueryEscape(relpath), nil)
	if err != nil {
		return digest.Digest{}, fmt.Errorf("abfs: get digest: new request: %w", err)
	}
	req.Header.Add("Content-Type", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return digest.Digest{}, fmt.Errorf("abfs: get digest: get request: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return digest.Digest{}, fmt.Errorf("abfs: get digest: read resp: %w", err)
	}
	switch resp.StatusCode {
	case http.StatusOK: // 200
	case http.StatusNotFound: // 404
		return digest.Digest{}, fmt.Errorf("abfs: get digest: not found %q: %w", fname, fs.ErrNotExist)
	default:
		return digest.Digest{}, fmt.Errorf("abfs: get digest: error %d: %s", resp.StatusCode, body)
	}

	var result RBEPathStat
	err = json.Unmarshal(body, &result)
	if err != nil {
		return digest.Digest{}, fmt.Errorf("abfs: get digest: parse resp %q: %w", body, err)
	}
	return digest.Digest{
		Hash:      result.SHA256,
		SizeBytes: result.Size,
	}, nil
}

func (c *Client) RegisterFiles(ctx context.Context, dir string, entries []*Registration) error {
	if c == nil || c.endpoint == "" || c.dir == "" || c.client == nil {
		return errors.ErrUnsupported
	}
	m := make(map[string]*Registration)
	var reqMsg SetRBEDigestsReq
	for _, ent := range entries {
		if ent == nil {
			continue
		}
		fullpath := filepath.Join(dir, string(ent.Entry.Name))
		relpath, err := filepath.Rel(c.dir, fullpath)
		if log.V(1) {
			clog.Infof(ctx, "abfs: entry %q %q -> %q: %v", dir, ent.Entry.Name, relpath, err)
		}
		if err != nil {
			ent.Err = fmt.Errorf("abfs: out of dir: %q", ent.Entry.Name)
			clog.Warningf(ctx, "%v", ent.Err)
			continue
		}
		if !filepath.IsLocal(relpath) {
			ent.Err = fmt.Errorf("abfs: out of dir: %q", ent.Entry.Name)
			clog.Warningf(ctx, "%v", ent.Err)
			continue
		}
		relpath = filepath.ToSlash(relpath)
		m[relpath] = ent
		d := ent.Entry.Data.Digest()
		if d.IsZero() {
			ent.Err = fmt.Errorf("abfs: empty digest: %q", ent.Entry.Name)
			clog.Warningf(ctx, "%v", ent.Err)
			continue
		}
		mode := fs.FileMode(syscall.S_IFREG | 0o644)
		if ent.Entry.IsExecutable {
			mode |= 0o111
		}
		reqMsg.Digests = append(reqMsg.Digests, RBEPathStat{
			Path:   relpath,
			SHA256: d.Hash,
			Size:   d.SizeBytes,
			Mode:   uint32(mode),
		})
	}
	reqBody, err := json.Marshal(reqMsg)
	if err != nil {
		return fmt.Errorf("abfs: set digests: marshal req: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint+"/mnt/set-rbe-digests", bytes.NewReader(reqBody))
	if err != nil {
		return fmt.Errorf("abfs: set digests: new request: %w", err)
	}
	req.Header.Add("Content-Type", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("abfs: set digests: post request: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("abfs: set digests: read resp: %w", err)
	}
	switch resp.StatusCode {
	case http.StatusOK:
	default:
		return fmt.Errorf("abfs: set digests: error %d: %s", resp.StatusCode, body)
	}
	if len(body) == 0 {
		return nil
	}
	var result map[string]string
	err = json.Unmarshal(body, &result)
	if err != nil {
		return fmt.Errorf("abfs: set digests: parse resp %q: %w", body, err)
	}
	for k, v := range result {
		ent, ok := m[k]
		if !ok {
			clog.Warningf(ctx, "abfs: set digests: resp unknown path %q: %s", k, v)
			continue
		}
		ent.Err = errors.New(v)
	}
	return nil
}
