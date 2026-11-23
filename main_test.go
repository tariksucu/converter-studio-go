package converterstudio_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	converterstudio "github.com/tariksucu/converter-studio-go"
)

// TestMain runs the examples against a fake API: it puts the fake's address and key in the
// environment (as a user would put the real ones) and runs everything in a folder of its own
// that holds the files the examples convert.
func TestMain(m *testing.M) {
	os.Exit(run(m))
}

func run(m *testing.M) int {
	api := newFakeAPI(nil)
	defer api.server.Close()
	api.statuses = []string{"done"}
	api.credits = func(name string) int {
		if strings.HasSuffix(name, ".mkv") {
			return 7
		}
		return 1
	}
	api.failWhen = func(name string) map[string]any {
		if strings.HasPrefix(name, "broken") {
			return map[string]any{"code": "conversionFailed",
				"message": "We couldn't convert this file. Your credits were given back."}
		}
		return nil
	}
	_ = os.Setenv(converterstudio.APIKeyEnv, testKey)
	_ = os.Setenv(converterstudio.BaseURLEnv, api.base)
	dir, err := os.MkdirTemp("", "converter-studio-go-examples-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer os.RemoveAll(dir)
	for _, name := range []string{"IMG_1234.HEIC", "talk.mov", "lecture.mkv", "scan.pdf", "song.flac", "broken.jpg"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	}
	back, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer os.Chdir(back) //nolint:errcheck
	return m.Run()
}
