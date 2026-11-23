package converterstudio_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	converterstudio "github.com/tariksucu/converter-studio-go"
)

// testKey is the only API key the fake API accepts.
const testKey = "cvs_live_test_key_0123456789"

// fakeAPI is a small stand-in for the Converter Studio API, served by httptest.
type fakeAPI struct {
	server *httptest.Server
	base   string // the API's address: the server's URL + "/api"

	mu sync.Mutex
	// statuses are those a new job's status requests go through, the last one staying.
	statuses   []string
	output     []byte
	outputName string
	// credits prices a file by its name.
	credits func(name string) int
	failure map[string]any
	// failWhen makes the conversion of some files fail.
	failWhen func(name string) map[string]any
	// holdWaits makes long polls take this long.
	holdWaits time.Duration
	requests  []recorded
	uploads   map[string]*fakeUpload
	jobs      map[string]*fakeJob
	queue     map[string][]answer
	idem      map[string]idemEntry
	counter   int
}

type recorded struct {
	Method  string
	Path    string
	RawPath string
	Query   url.Values
	Header  http.Header
	Body    []byte
}

type fakeUpload struct {
	name string
	size int64
	data []byte
}

type fakeJob struct {
	id, to, name string
	size         int64
	statuses     []string
	credits      int
	maxCredits   *int
	failure      map[string]any
	output       []byte
	outputName   string
}

type idemEntry struct {
	jobID, fingerprint string
}

// An answer is a scripted reply, given instead of the usual one.
type answer func(f *fakeAPI, w http.ResponseWriter, r *http.Request, body []byte)

func newFakeAPI(t testing.TB) *fakeAPI {
	f := &fakeAPI{
		statuses: []string{"processing", "done"},
		output:   []byte("RESULT"),
		credits:  func(string) int { return 1 },
		uploads:  map[string]*fakeUpload{},
		jobs:     map[string]*fakeJob{},
		queue:    map[string][]answer{},
		idem:     map[string]idemEntry{},
	}
	f.server = httptest.NewServer(http.HandlerFunc(f.handle))
	f.base = f.server.URL + "/api"
	if t != nil {
		t.Cleanup(f.server.Close)
	}
	return f
}

// client is a client of the fake API with the right key; pauses are skipped and recorded.
func (f *fakeAPI) client(t testing.TB, opts ...converterstudio.Option) (*converterstudio.Client, *[]time.Duration) {
	t.Helper()
	all := append([]converterstudio.Option{
		converterstudio.WithAPIKey(testKey), converterstudio.WithBaseURL(f.base),
	}, opts...)
	c, err := converterstudio.NewClient(all...)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	pauses := &[]time.Duration{}
	converterstudio.SetSleep(c, func(ctx context.Context, d time.Duration) error {
		mu.Lock()
		*pauses = append(*pauses, d)
		mu.Unlock()
		return ctx.Err()
	})
	return c, pauses
}

// inProcess is an HTTP client that hands each request straight to the fake's handler, in the
// caller's goroutine, with no network in between. It is for tests in a synctest bubble: their
// clock is fake and moves only while every goroutine of the bubble is blocked, and one waiting
// on the network never counts as blocked.
func (f *fakeAPI) inProcess() *http.Client {
	return &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if err := r.Context().Err(); err != nil {
			return nil, err
		}
		in := r.Clone(r.Context())
		if in.Body == nil {
			in.Body = http.NoBody
		}
		answer := httptest.NewRecorder()
		f.handle(answer, in)
		return answer.Result(), nil
	})}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (rt roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return rt(r) }

// push queues answers for a path ("/api/v1/conversions"), given before the usual ones.
func (f *fakeAPI) push(path string, answers ...answer) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queue[path] = append(f.queue[path], answers...)
}

// of returns the requests with this method and path.
func (f *fakeAPI) of(method, path string) []recorded {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []recorded
	for _, r := range f.requests {
		if r.Method == method && r.Path == path {
			out = append(out, r)
		}
	}
	return out
}

func (f *fakeAPI) all() []recorded {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]recorded(nil), f.requests...)
}

func (f *fakeAPI) handle(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.requests = append(f.requests, recorded{r.Method, r.URL.Path, r.URL.EscapedPath(), r.URL.Query(), r.Header.Clone(), body})
	var next answer
	if queued := f.queue[r.URL.Path]; len(queued) > 0 {
		next, f.queue[r.URL.Path] = queued[0], queued[1:]
	}
	f.mu.Unlock()
	if next != nil {
		next(f, w, r, body)
		return
	}
	f.route(w, r, body)
}

// respond answers with a status, a JSON body and header pairs.
func respond(status int, body string, header ...string) answer {
	return func(_ *fakeAPI, w http.ResponseWriter, _ *http.Request, _ []byte) {
		for i := 0; i+1 < len(header); i += 2 {
			w.Header().Set(header[i], header[i+1])
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}
}

func apiError(status int, code, message string) answer {
	return respond(status, fmt.Sprintf(`{"error":{"code":%q,"message":%q}}`, code, message))
}

// hangUp closes the connection without an answer.
func hangUp() answer {
	return func(_ *fakeAPI, w http.ResponseWriter, _ *http.Request, _ []byte) {
		conn, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			_ = conn.Close()
		}
	}
}

// loseAnswer does what the request asks, then closes the connection without the answer.
func loseAnswer() answer {
	return func(f *fakeAPI, w http.ResponseWriter, r *http.Request, body []byte) {
		f.route(httptest.NewRecorder(), r, body)
		hangUp()(f, w, r, body)
	}
}

// stall answers nothing until the client gives up.
func stall() answer {
	return func(_ *fakeAPI, _ http.ResponseWriter, r *http.Request, _ []byte) {
		<-r.Context().Done()
	}
}

// cutShort sends the start of a body longer than announced, then closes the connection.
func cutShort(data []byte) answer {
	return func(_ *fakeAPI, w http.ResponseWriter, _ *http.Request, _ []byte) {
		w.Header().Set("Content-Length", strconv.Itoa(len(data)*2))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data)
		w.(http.Flusher).Flush()
		panic(http.ErrAbortHandler)
	}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]any{"code": code, "message": message}})
}

func (f *fakeAPI) nextID(prefix string) string {
	f.counter++
	return fmt.Sprintf("%s%08d", prefix, f.counter)
}

func (f *fakeAPI) route(w http.ResponseWriter, r *http.Request, body []byte) {
	p := strings.TrimPrefix(r.URL.Path, "/api")
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case r.Method == http.MethodGet && p == "/v1/formats":
		formats := []map[string]any{
			{"id": "heic", "name": "HEIC", "category": "image", "extensions": []string{"heic", "heif"}, "to": []string{"jpg", "png", "webp"}},
			{"id": "mov", "name": "QuickTime", "category": "video", "extensions": []string{"mov"}, "to": []string{"mp4", "webm"}},
		}
		from := r.URL.Query().Get("from")
		if from == "nope" {
			writeError(w, 400, "invalidRequest", "Something about that request wasn't right.")
			return
		}
		out := []map[string]any{}
		for _, format := range formats {
			if from == "" || format["id"] == from {
				out = append(out, format)
			}
		}
		writeJSON(w, 200, map[string]any{"formats": out})
		return
	case strings.HasPrefix(p, "/v1/uploads/") && r.Method == http.MethodPut:
		f.receive(w, r, path.Base(p), body)
		return
	case strings.HasPrefix(p, "/jobs/") && r.Method == http.MethodGet:
		job := f.jobs[strings.Split(p, "/")[2]]
		if job == nil || r.URL.Query().Get("token") != "tok_"+job.id {
			writeJSON(w, 404, map[string]any{"error": "notFound"})
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(job.output)
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+testKey {
		writeError(w, 401, "invalidApiKey", "This API key does not work. Create one on your account page at converter.studio.")
		return
	}
	switch {
	case r.Method == http.MethodGet && p == "/v1/account":
		writeJSON(w, 200, map[string]any{
			"email": "dev@example.com", "plan": "pro", "allApps": true, "credits": 1099,
			"allowance": map[string]any{"used": 1, "limit": 1100, "per": "month"}, "packs": 0,
			"renewsAt": "2026-12-22T01:16:48.716Z", "maxFileBytes": 1 << 30, "maxMediaSeconds": 7200,
			"wallet": map[string]any{"balanceMicros": 1500000, "pays": true}, "newField": "kept",
		})
	case r.Method == http.MethodPost && p == "/v1/uploads":
		var in struct {
			FileName string `json:"fileName"`
			Size     *int64 `json:"size"`
		}
		_ = json.Unmarshal(body, &in)
		id := f.nextID("up")
		upload := &fakeUpload{name: in.FileName, size: -1}
		if in.Size != nil {
			upload.size = *in.Size
		}
		f.uploads[id] = upload
		writeJSON(w, 201, map[string]any{
			"id": id, "uploadUrl": fmt.Sprintf("%s/v1/uploads/%s?token=tok_%s", f.base, id, id),
			"method": "PUT", "maxBytes": 1 << 30, "partBytes": 95 << 20,
			"expiresAt": "2026-11-23T02:00:00.000Z", "curl": "curl ...",
		})
	case r.Method == http.MethodPost && p == "/v1/conversions":
		f.create(w, r, body)
	case strings.HasPrefix(p, "/v1/conversions/"):
		parts := strings.Split(p, "/")
		job := f.jobs[parts[3]]
		if job == nil {
			writeError(w, 404, "notFound", "We couldn't find that conversion.")
			return
		}
		if r.Method == http.MethodPost && len(parts) == 5 && parts[4] == "confirm" {
			job.statuses = []string{"queued", "done"}
			writeJSON(w, 200, job.view(f.base))
			return
		}
		if r.URL.Query().Get("wait") != "" && f.holdWaits > 0 {
			f.mu.Unlock()
			time.Sleep(f.holdWaits)
			f.mu.Lock()
		}
		view := job.view(f.base)
		if len(job.statuses) > 1 {
			job.statuses = job.statuses[1:]
		}
		writeJSON(w, 200, view)
	default:
		writeJSON(w, 404, map[string]any{"error": "notFound"})
	}
}

func (f *fakeAPI) receive(w http.ResponseWriter, r *http.Request, id string, body []byte) {
	upload := f.uploads[id]
	if upload == nil || r.URL.Query().Get("token") != "tok_"+id {
		writeError(w, 404, "notFound", "We couldn't find that conversion.")
		return
	}
	if offset := r.Header.Get("Upload-Offset"); offset != "" {
		n, _ := strconv.Atoi(offset)
		upload.data = append(upload.data[:min(n, len(upload.data))], body...)
	} else {
		upload.data = body
	}
	status := "receiving"
	if upload.size < 0 || int64(len(upload.data)) >= upload.size {
		status = "ready"
	}
	writeJSON(w, 200, map[string]any{"id": id, "status": status})
}

func (f *fakeAPI) create(w http.ResponseWriter, r *http.Request, body []byte) {
	var in map[string]any
	if err := json.Unmarshal(body, &in); err != nil {
		writeError(w, 400, "invalidRequest", "Something about that request wasn't right.")
		return
	}
	key := r.Header.Get("Idempotency-Key")
	fingerprint, _ := json.Marshal(in)
	if entry, ok := f.idem[key]; key != "" && ok {
		if entry.fingerprint != string(fingerprint) {
			writeError(w, 409, "idempotencyKeyReused", "This Idempotency-Key was already used for another request.")
			return
		}
		w.Header().Set("Idempotent-Replayed", "true")
		writeJSON(w, 200, f.jobs[entry.jobID].view(f.base))
		return
	}
	job := &fakeJob{to: in["to"].(string), statuses: append([]string(nil), f.statuses...),
		failure: f.failure, output: f.output, outputName: f.outputName}
	if uploadID, ok := in["uploadId"].(string); ok {
		upload := f.uploads[uploadID]
		if upload == nil {
			writeError(w, 404, "notFound", "We couldn't find that conversion.")
			return
		}
		delete(f.uploads, uploadID)
		job.id, job.name, job.size = uploadID, upload.name, int64(len(upload.data))
	} else {
		link, _ := in["url"].(string)
		job.id, job.name = f.nextID("ln"), path.Base(link)
	}
	job.credits = f.credits(job.name)
	if f.failWhen != nil {
		if failure := f.failWhen(job.name); failure != nil {
			job.failure, job.statuses = failure, []string{"failed"}
		}
	}
	maxCredits := 20
	if v, ok := in["maxCredits"].(float64); ok {
		maxCredits = int(v)
	}
	job.maxCredits = &maxCredits
	if job.credits > maxCredits {
		job.statuses = []string{"quoted"}
	}
	f.jobs[job.id] = job
	if key != "" {
		f.idem[key] = idemEntry{job.id, string(fingerprint)}
	}
	view := job.view(f.base)
	if _, ok := in["url"]; ok {
		view["status"] = "fetching"
	} else {
		view["status"] = "queued"
	}
	writeJSON(w, 201, view)
}

func (j *fakeJob) view(base string) map[string]any {
	status := j.statuses[0]
	done := status == "done"
	stem := strings.TrimSuffix(j.name, path.Ext(j.name))
	outputName := j.outputName
	if outputName == "" {
		outputName = stem + "." + j.to
	}
	view := map[string]any{
		"id": j.id, "status": status, "from": strings.ToLower(strings.TrimPrefix(path.Ext(j.name), ".")),
		"to": j.to, "inputName": j.name, "inputSize": j.size, "credits": j.credits,
		"maxCredits": j.maxCredits, "progress": nil, "outputName": outputName, "outputSize": nil,
		"downloadUrl": nil, "expiresAt": "2026-11-23T07:00:00.000Z", "error": nil, "next": "Check again.",
	}
	if done {
		view["progress"], view["outputSize"], view["next"] = 1, len(j.output), "Download it."
		view["downloadUrl"] = fmt.Sprintf("%s/jobs/%s/download?token=tok_%s", base, j.id, j.id)
	}
	if status == "failed" {
		view["error"] = j.failure
	}
	return view
}
