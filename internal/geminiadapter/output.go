package geminiadapter

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"log/slog"
)

// maxOutputLineBytes bounds one stdout frame of the Gemini CLI. Tool results
// and resumed history routinely exceed bufio.Scanner's limits; this cap only
// keeps runaway output from exhausting the Agent's memory.
const maxOutputLineBytes = 64 << 20

var errOutputLineTooLong = errors.New("Gemini CLI output line exceeds 64 MiB")

// lineReader reads newline-delimited frames of any size up to its limit.
type lineReader struct {
	reader *bufio.Reader
	limit  int
}

func newLineReader(r io.Reader) *lineReader {
	return &lineReader{reader: bufio.NewReaderSize(r, 64<<10), limit: maxOutputLineBytes}
}

// next returns the next line without its line ending (the last line may lack
// a trailing newline) and io.EOF at the end of the input. A line longer than
// the limit is an error: the turn cannot be trusted to continue past it.
func (l *lineReader) next() ([]byte, error) {
	var line []byte
	for {
		chunk, err := l.reader.ReadSlice('\n')
		if len(line)+len(chunk) > l.limit {
			return nil, errOutputLineTooLong
		}
		line = append(line, chunk...)
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if err == nil || (errors.Is(err, io.EOF) && len(line) > 0) {
			return bytes.TrimRight(line, "\r\n"), nil
		}
		return nil, err
	}
}

// skippedOutput accounts for stdout lines that are not JSON frames, such as a
// stray console.log from the CLI, an extension or a Node library. They must
// not fail the turn. Their content is never logged: it may contain provider
// diagnostics or credentials.
type skippedOutput struct {
	mode  string
	lines int
}

func (s *skippedOutput) add(line []byte) {
	s.lines++
	if s.lines == 1 {
		slog.Default().Warn("skipping non-JSON Gemini CLI output", "mode", s.mode, "bytes", len(line))
	}
}

func (s *skippedOutput) report() {
	if s.lines > 1 {
		slog.Default().Warn("skipped non-JSON Gemini CLI output", "mode", s.mode, "lines", s.lines)
	}
}
