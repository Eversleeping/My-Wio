package childprocess

import (
	"io"
	"os/exec"
	"sync"
)

// Stream is a started command whose stdout is read incrementally.
//
// exec.Cmd.StdoutPipe requires reading to EOF before calling Wait. EOF never
// arrives while a descendant still holds the pipe, so the command would stay
// an unreaped zombie and the reader would block indefinitely. Stream calls
// Wait as soon as the command starts; once the command exits and cmd.WaitDelay
// elapses, the inherited pipe is closed and readers see EOF.
type Stream struct {
	reader *io.PipeReader
	done   chan error
	once   sync.Once
	err    error
}

// StartStream starts cmd with stdout delivered through the returned Stream.
// cmd.Stdout must be unset and cmd.WaitDelay should be set; without it a
// descendant holding stdout still delays EOF until it exits.
func StartStream(cmd *exec.Cmd) (*Stream, error) {
	reader, writer := io.Pipe()
	cmd.Stdout = writer
	if err := cmd.Start(); err != nil {
		_ = reader.Close()
		_ = writer.Close()
		return nil, err
	}
	stream := &Stream{reader: reader, done: make(chan error, 1)}
	go func() {
		err := cmd.Wait()
		_ = writer.Close()
		stream.done <- err
	}()
	return stream, nil
}

func (s *Stream) Read(p []byte) (int, error) { return s.reader.Read(p) }

// Wait stops reading stdout and returns the command's Wait result. Callers
// that stop before EOF should end the command first (for example by
// cancelling its context); unread output is discarded.
func (s *Stream) Wait() error {
	s.once.Do(func() {
		_ = s.reader.Close()
		s.err = Result(<-s.done)
	})
	return s.err
}
