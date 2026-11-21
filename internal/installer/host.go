package installer

import (
	"context"
	"io"
	"runtime"

	"github.com/wio-platform/wio/internal/childprocess"
	"golang.org/x/crypto/ssh"
)

type commandHost interface {
	execute(string, io.Reader, io.Writer) error
}
type sshHost struct{ *ssh.Client }

func (h *sshHost) execute(command string, input io.Reader, output io.Writer) error {
	session, err := h.NewSession()
	if err != nil {
		return err
	}
	defer session.Close()
	session.Stdin, session.Stdout, session.Stderr = input, output, output
	return session.Run(command)
}
func executeCommand(h commandHost, command string, input io.Reader, output io.Writer) error {
	return h.execute(command, input, output)
}

type localHost struct{ ctx context.Context }

func (h localHost) execute(command string, input io.Reader, output io.Writer) error {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		return ErrUnsupportedPlatform
	}
	cmd := childprocess.CommandContext(childprocess.WithScope(h.ctx, childprocess.Installation), "sh", "-c", command)
	configureInstallerProcess(cmd)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = input, output, output
	return childprocess.Result(childprocess.Run(cmd))
}
