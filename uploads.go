package converterstudio

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
)

// A reader that cannot seek is kept in memory up to this size, and in a temporary file above.
const spoolBytes = 8 << 20

// UploadOptions are the settings of [Client.UploadFile] and [Client.UploadReader].
type UploadOptions struct {
	// PartSize makes the parts of a file smaller than the API's limit (95 MB; files larger
	// than one part are sent in parts on their own). 0: the API's limit.
	PartSize int64
}

// CreateUpload opens an upload slot for a file of size bytes (POST /v1/uploads; size < 0:
// not known). [Client.UploadFile] and [Client.UploadReader] do this and send the file.
func (c *Client) CreateUpload(ctx context.Context, fileName string, size int64) (*Upload, error) {
	if fileName == "" {
		return nil, errors.New("converterstudio: an upload needs a file name (such as \"photo.heic\")")
	}
	body := map[string]any{"fileName": fileName}
	if size >= 0 {
		body["size"] = size
	}
	k := &call{method: http.MethodPost, url: c.apiURL("/v1/uploads", nil), body: body, auth: true}
	data, _, err := c.callJSON(ctx, k)
	if err != nil {
		return nil, err
	}
	var upload Upload
	if err := decodeJSON(data, &upload); err != nil {
		return nil, err
	}
	if upload.ID == "" || upload.UploadURL == "" {
		return nil, errors.New("converterstudio: the API's upload slot has no ID or link")
	}
	return &upload, nil
}

// UploadFile sends a local file and returns its upload; convert it with
// [Client.CreateConversion] (UploadID: upload.ID). Files larger than one request carries
// (95 MB) go in parts, and a part that fails is sent again. opts may be nil.
func (c *Client) UploadFile(ctx context.Context, path string, opts *UploadOptions) (*Upload, error) {
	src, err := fileSource(path)
	if err != nil {
		return nil, err
	}
	defer src.close()
	return c.upload(ctx, src, opts)
}

// UploadReader sends what r reads as a file named fileName (the API reads the format from the
// bytes and the name) and returns its upload. fileName may be empty for an [*os.File]. A
// reader that can seek and read at an offset ([*os.File], [*bytes.Reader],
// [*strings.Reader]) is sent from where it stands; any other is read first, into memory up to
// 8 MB and into a temporary file above. opts may be nil.
func (c *Client) UploadReader(ctx context.Context, r io.Reader, fileName string, opts *UploadOptions) (*Upload, error) {
	src, err := readerSource(r, fileName)
	if err != nil {
		return nil, err
	}
	defer src.close()
	return c.upload(ctx, src, opts)
}

func (c *Client) upload(ctx context.Context, src *source, opts *UploadOptions) (*Upload, error) {
	slot, err := c.CreateUpload(ctx, src.name, src.size)
	if err != nil {
		return nil, err
	}
	partBytes := slot.PartBytes
	if partBytes <= 0 {
		partBytes = defaultPartBytes
	}
	if opts != nil && opts.PartSize > 0 && opts.PartSize < partBytes {
		partBytes = opts.PartSize
	}
	ranges := partRanges(src.size, partBytes)
	for _, r := range ranges {
		k := &call{
			method:   http.MethodPut,
			url:      slot.UploadURL,
			part:     &part{src: src.at, offset: src.base + r[0], length: r[1]},
			partDone: true,
		}
		if len(ranges) > 1 {
			k.header = map[string]string{"Upload-Offset": strconv.FormatInt(r[0], 10)}
		}
		// The answer is read, so that the connection is kept for the next part.
		drain := func(resp *http.Response) error {
			_, err := io.Copy(io.Discard, io.LimitReader(resp.Body, maxJSONBytes))
			return err
		}
		if err := c.retry(ctx, k, drain); err != nil {
			return nil, err
		}
	}
	return slot, nil
}

// partRanges is the offset and length of each part; one empty part for an empty file.
func partRanges(size, partBytes int64) [][2]int64 {
	if size <= partBytes {
		return [][2]int64{{0, size}}
	}
	var ranges [][2]int64
	for offset := int64(0); offset < size; offset += partBytes {
		ranges = append(ranges, [2]int64{offset, min(partBytes, size-offset)})
	}
	return ranges
}

// A source is a file to upload, readable at any offset so that a part can be sent again.
type source struct {
	name  string
	size  int64
	at    io.ReaderAt
	base  int64
	close func()
}

func fileSource(path string) (*source, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	if !info.Mode().IsRegular() {
		_ = f.Close()
		return nil, fmt.Errorf("converterstudio: %s is not a file", path)
	}
	return &source{
		name: filepath.Base(path), size: info.Size(), at: f, close: func() { _ = f.Close() },
	}, nil
}

func readerSource(r io.Reader, name string) (*source, error) {
	if r == nil {
		return nil, errors.New("converterstudio: UploadReader needs a reader")
	}
	if name == "" {
		if f, ok := r.(*os.File); ok {
			name = filepath.Base(f.Name())
		}
	}
	if name == "" {
		return nil, errors.New("converterstudio: a file name is needed with a reader (such as \"photo.heic\")")
	}
	type seekReaderAt interface {
		io.ReaderAt
		io.Seeker
	}
	if s, ok := r.(seekReaderAt); ok {
		start, err := s.Seek(0, io.SeekCurrent)
		if err == nil {
			var end int64
			if end, err = s.Seek(0, io.SeekEnd); err == nil {
				_, err = s.Seek(start, io.SeekStart)
			}
			if err == nil {
				return &source{name: name, size: end - start, at: s, base: start, close: func() {}}, nil
			}
		}
		// A pipe or a terminal: read it like any other reader.
	}
	head, err := io.ReadAll(io.LimitReader(r, spoolBytes+1))
	if err != nil {
		return nil, err
	}
	if len(head) <= spoolBytes {
		return &source{name: name, size: int64(len(head)), at: bytes.NewReader(head), close: func() {}}, nil
	}
	tmp, err := os.CreateTemp("", "converter-studio-upload-*")
	if err != nil {
		return nil, err
	}
	cleanup := func() {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
	}
	_, err = tmp.Write(head)
	var rest int64
	if err == nil {
		rest, err = io.Copy(tmp, r)
	}
	if err != nil {
		cleanup()
		return nil, err
	}
	return &source{name: name, size: int64(len(head)) + rest, at: tmp, close: cleanup}, nil
}
