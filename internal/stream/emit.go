package stream

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
)

// Writer emits fast-import stream tokens. It pairs with Scanner: tokens
// that a pipeline leaves untouched are written back verbatim via Raw and
// CopyData, so unmodified regions round-trip byte-for-byte.
type Writer struct {
	bw *bufio.Writer
}

// NewWriter wraps w.
func NewWriter(w io.Writer) *Writer {
	return &Writer{bw: bufio.NewWriter(w)}
}

// Raw writes one line verbatim, newline included.
func (w *Writer) Raw(line string) error {
	if _, err := w.bw.WriteString(line); err != nil {
		return err
	}
	return w.bw.WriteByte('\n')
}

// Data writes a data block from an in-memory payload (messages, tag
// annotations). Large payloads must go through CopyData instead.
func (w *Writer) Data(payload string) error {
	if err := w.Raw("data " + strconv.Itoa(len(payload))); err != nil {
		return err
	}
	_, err := w.bw.WriteString(payload)
	return err
}

// CopyData writes tok's header followed by its payload, streaming exactly
// tok.Size bytes out of tok.Body without buffering them.
func (w *Writer) CopyData(tok Token) error {
	if err := w.Raw(tok.Raw); err != nil {
		return err
	}
	n, err := io.Copy(w.bw, io.LimitReader(tok.Body, tok.Size))
	if err != nil {
		return fmt.Errorf("copy %d-byte data block: %w", tok.Size, err)
	}
	if n != tok.Size {
		return fmt.Errorf("short data block: got %d of %d bytes", n, tok.Size)
	}
	return nil
}

// Flush pushes buffered bytes to the underlying writer.
func (w *Writer) Flush() error {
	return w.bw.Flush()
}

// Drain discards exactly tok.Size bytes of tok.Body. Callers that replace
// a payload use it to skip the original bytes in lockstep.
func Drain(tok Token) error {
	n, err := io.CopyN(io.Discard, tok.Body, tok.Size)
	if err != nil {
		return fmt.Errorf("discard %d-byte data block: %w", tok.Size, err)
	}
	if n != tok.Size {
		return fmt.Errorf("short data block: discarded %d of %d bytes", n, tok.Size)
	}
	return nil
}
