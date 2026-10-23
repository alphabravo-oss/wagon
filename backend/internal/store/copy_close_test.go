package store

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

type fakeWriteCloser struct {
	buf      bytes.Buffer
	writeErr error
	closeErr error
	closed   bool
}

func (f *fakeWriteCloser) Write(p []byte) (int, error) {
	if f.writeErr != nil {
		return 0, f.writeErr
	}
	return f.buf.Write(p)
}

func (f *fakeWriteCloser) Close() error {
	f.closed = true
	return f.closeErr
}

func TestCopyAndCloseReturnsCloseError(t *testing.T) {
	closeErr := errors.New("flush failed")
	w := &fakeWriteCloser{closeErr: closeErr}
	n, err := copyAndClose(w, strings.NewReader("archive-bytes"))
	if !errors.Is(err, closeErr) {
		t.Fatalf("expected close error, got %v", err)
	}
	if n != int64(len("archive-bytes")) {
		t.Fatalf("written = %d", n)
	}
	if !w.closed {
		t.Fatal("dst not closed")
	}
}

func TestCopyAndCloseReturnsCopyErrorAndCloses(t *testing.T) {
	writeErr := errors.New("write failed")
	w := &fakeWriteCloser{writeErr: writeErr, closeErr: errors.New("close failed")}
	_, err := copyAndClose(w, strings.NewReader("x"))
	if !errors.Is(err, writeErr) {
		t.Fatalf("expected copy error to take precedence, got %v", err)
	}
	if !w.closed {
		t.Fatal("dst not closed after copy error")
	}
}

func TestCopyAndCloseSuccess(t *testing.T) {
	w := &fakeWriteCloser{}
	n, err := copyAndClose(w, strings.NewReader("hello"))
	if err != nil || n != 5 || w.buf.String() != "hello" || !w.closed {
		t.Fatalf("n=%d err=%v buf=%q closed=%v", n, err, w.buf.String(), w.closed)
	}
}
