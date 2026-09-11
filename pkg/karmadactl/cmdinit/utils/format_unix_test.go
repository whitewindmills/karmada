//go:build unix

/*
Copyright 2026 The Karmada Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package utils

import (
	"bytes"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestFileToBytesReadsStreamBackedFile(t *testing.T) {
	directory := t.TempDir()
	const name = "certificate.fifo"
	path := filepath.Join(directory, name)
	if err := syscall.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	want := []byte("streamed certificate data")
	writerDone := make(chan error, 1)
	go func() {
		writerDone <- os.WriteFile(path, want, 0600)
	}()
	got, err := FileToBytes(directory, name)
	if err != nil {
		t.Errorf("FileToBytes() failed: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("FileToBytes() = %q, want %q", got, want)
	}
	if err := <-writerDone; err != nil {
		t.Errorf("stream writer failed: %v", err)
	}
}
