package converterstudio

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// DownloadOptions are the settings of [Client.DownloadFile].
type DownloadOptions struct {
	// Overwrite replaces an existing file. Without it, a result saved into a folder gets -1,
	// -2... before its extension, and an exact path that exists fails with [fs.ErrExist].
	Overwrite bool
}

// Download writes the result of a done conversion to w and returns the number of bytes
// written. The result is streamed: nothing is kept in memory. It is sent again after a
// network error only while nothing has been written to w yet.
//
// A conversion that is not done fails with [ErrNotDone], one that failed with a
// [*ConversionFailedError]. Results are kept for 6 hours ([Conversion.ExpiresAt]); a deleted
// one fails with [ErrGone].
func (c *Client) Download(ctx context.Context, conversion *Conversion, w io.Writer) (int64, error) {
	k, err := downloadCall(conversion)
	if err != nil {
		return 0, err
	}
	var written int64
	err = c.retry(ctx, k, func(resp *http.Response) error {
		n, err := io.Copy(w, resp.Body)
		written += n
		if err != nil && n > 0 {
			return &finalError{err}
		}
		return err
	})
	return written, err
}

// DownloadFile saves the result of a done conversion and returns the file's path.
//
// dest is a folder (one that exists, or a path ending in a slash), into which the result goes
// under its own name; or the path of the file to write; or "" for the current folder. In a
// folder, an existing file is never replaced unless opts.Overwrite: the result gets -1, -2...
// before its extension. The file appears only once it is complete (it is written under a
// temporary name first), and a broken download is started again. opts may be nil.
func (c *Client) DownloadFile(ctx context.Context, conversion *Conversion, dest string, opts *DownloadOptions) (string, error) {
	k, err := downloadCall(conversion)
	if err != nil {
		return "", err
	}
	overwrite := opts != nil && opts.Overwrite
	name := safeFileName(conversion.OutputName, conversion.ID+"."+orDefault(conversion.To, "bin"))
	path, exact := targetPath(dest, name)
	if exact && !overwrite {
		if _, err := os.Lstat(path); err == nil {
			return "", &fs.PathError{Op: "download", Path: path, Err: fs.ErrExist}
		}
	}
	if !overwrite {
		if path, err = freePath(path); err != nil {
			return "", err
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	tmp, err := createTemporary(path)
	if err != nil {
		return "", err
	}
	err = c.retry(ctx, k, func(resp *http.Response) error {
		if _, err := tmp.Seek(0, io.SeekStart); err != nil {
			return err
		}
		if err := tmp.Truncate(0); err != nil {
			return err
		}
		_, err := io.Copy(tmp, resp.Body)
		return err
	})
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), path)
	}
	if err != nil {
		_ = os.Remove(tmp.Name())
		return "", err
	}
	return path, nil
}

func downloadCall(conversion *Conversion) (*call, error) {
	if conversion == nil {
		return nil, errors.New("converterstudio: a conversion is needed")
	}
	if err := settledError(conversion); err != nil {
		return nil, err
	}
	if conversion.DownloadURL == "" {
		return nil, errors.New("converterstudio: the conversion has no download link")
	}
	return &call{method: http.MethodGet, url: conversion.DownloadURL, follow: true}, nil
}

// safeFileName is a name to save a result under: its last part only, without control
// characters or slashes, or fallback.
func safeFileName(name, fallback string) string {
	name = strings.ReplaceAll(name, "\\", "/")
	name = name[strings.LastIndex(name, "/")+1:]
	name = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return '_'
		}
		return r
	}, name)
	name = strings.Trim(strings.TrimSpace(name), ".")
	if name == "" {
		return fallback
	}
	return name
}

// targetPath is where a result goes, and whether dest named the file itself.
func targetPath(dest, name string) (string, bool) {
	if dest == "" {
		return name, false
	}
	if strings.HasSuffix(dest, "/") || strings.HasSuffix(dest, string(os.PathSeparator)) {
		return filepath.Join(dest, name), false
	}
	if info, err := os.Stat(dest); err == nil && info.IsDir() {
		return filepath.Join(dest, name), false
	}
	return dest, true
}

// freePath is path, or with -1, -2... before its extension when a file is already there.
func freePath(path string) (string, error) {
	if _, err := os.Lstat(path); errors.Is(err, fs.ErrNotExist) {
		return path, nil
	}
	dir, base := filepath.Split(path)
	ext := filepath.Ext(base)
	if strings.HasSuffix(base, ".tar.gz") {
		ext = ".tar.gz"
	}
	stem := strings.TrimSuffix(base, ext)
	for n := 1; n < 10000; n++ {
		candidate := filepath.Join(dir, stem+"-"+strconv.Itoa(n)+ext)
		if _, err := os.Lstat(candidate); errors.Is(err, fs.ErrNotExist) {
			return candidate, nil
		}
	}
	return "", &fs.PathError{Op: "download", Path: path, Err: errors.New("no free name")}
}

// createTemporary opens a new hidden file next to path, with the usual permissions.
func createTemporary(path string) (*os.File, error) {
	dir, base := filepath.Split(path)
	for range 100 {
		b := make([]byte, 4)
		_, _ = rand.Read(b)
		name := filepath.Join(dir, "."+base+"."+hex.EncodeToString(b)+".part")
		f, err := os.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o666)
		if !errors.Is(err, fs.ErrExist) {
			return f, err
		}
	}
	return nil, &fs.PathError{Op: "download", Path: path, Err: errors.New("no free temporary name")}
}

func orDefault(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
