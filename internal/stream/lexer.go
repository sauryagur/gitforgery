package stream

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// Kind classifies a scanned stream token.
type Kind uint8

const (
	// KindCommand opens a block: blob, commit, tag, reset, checkpoint,
	// progress, feature, option, alias or done.
	KindCommand Kind = iota + 1
	// KindHeader is a key/value line inside a block (mark, original-oid,
	// author, committer, tagger, encoding, from, merge).
	KindHeader
	// KindData announces a length-prefixed payload. Token.Size bytes are
	// available through Token.Body.
	KindData
	// KindOther is any other line (file modify/delete ops inside commit
	// blocks, comments). It passes through verbatim.
	KindOther
)

var commandVerbs = map[string]bool{
	"blob": true, "commit": true, "tag": true, "reset": true,
	"checkpoint": true, "progress": true, "feature": true,
	"option": true, "alias": true, "done": true,
}

var headerVerbs = map[string]bool{
	"mark": true, "original-oid": true, "author": true, "committer": true,
	"tagger": true, "encoding": true, "from": true, "merge": true,
}

// Token is one scanned element of a fast-import stream.
type Token struct {
	Kind Kind
	// Verb is the first word of the line ("commit", "author", ...).
	Verb string
	// Arg is everything after the verb and one separating space.
	Arg string
	// Raw is the verbatim line without its trailing newline.
	Raw string

	// Size is the payload byte count; Body yields exactly that many
	// bytes. Both are meaningful only for KindData.
	Size int64
	Body io.Reader
}

// Scanner reads a fast-import stream incrementally.
//
// Data-block payloads are never buffered here: each KindData token wraps
// the underlying reader in an io.LimitedReader, so callers stream blobs of
// any size through in bounded memory.
//
// Contract: between two Next calls the previous KindData token's Body must
// be drained exactly Size bytes (or the whole pipeline abandoned);
// otherwise the scanner and the stream desynchronize.
type Scanner struct {
	br *bufio.Reader
}

// NewScanner wraps r.
func NewScanner(r io.Reader) *Scanner {
	return &Scanner{br: bufio.NewReader(r)}
}

// Next scans the next token. It returns io.EOF at a clean end of stream.
func (s *Scanner) Next() (Token, error) {
	line, err := s.readLine()
	if err != nil {
		return Token{}, err
	}
	verb, arg, _ := strings.Cut(line, " ")
	switch {
	case verb == "data":
		n, perr := parseCount(arg)
		if perr != nil {
			return Token{}, perr
		}
		return Token{
			Kind: KindData, Verb: verb, Arg: arg, Raw: line,
			Size: n, Body: io.LimitReader(s.br, n),
		}, nil
	case commandVerbs[verb]:
		return Token{Kind: KindCommand, Verb: verb, Arg: arg, Raw: line}, nil
	case headerVerbs[verb]:
		return Token{Kind: KindHeader, Verb: verb, Arg: arg, Raw: line}, nil
	default:
		return Token{Kind: KindOther, Verb: verb, Arg: arg, Raw: line}, nil
	}
}

// readLine returns the next line without its trailing newline. A final
// line without newline is returned as-is; a bare EOF yields io.EOF.
func (s *Scanner) readLine() (string, error) {
	line, err := s.br.ReadString('\n')
	if err != nil && len(line) == 0 {
		return "", err // clean io.EOF or hard reader failure
	}
	return strings.TrimSuffix(line, "\n"), nil
}

// parseCount parses the byte count of a "data <count>" line.
func parseCount(arg string) (int64, error) {
	n, err := strconv.ParseInt(strings.TrimSpace(arg), 10, 63)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("invalid data count %q", arg)
	}
	return n, nil
}
