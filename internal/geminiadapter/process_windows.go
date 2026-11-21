package geminiadapter

import (
	"github.com/wio-platform/wio/internal/childprocess"
	"os/exec"
)

func configureProcess(cmd *exec.Cmd) { childprocess.Configure(cmd) }
