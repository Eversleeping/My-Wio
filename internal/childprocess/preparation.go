package childprocess

import (
	"context"
	"os/exec"
)

type commandPreparerKey struct{}

// CommandPreparer applies an optional boundary after the command's arguments,
// environment and descriptors are final. Release belongs to the process owner
// and must run after Wait, or immediately if Start fails.
type CommandPreparer func(context.Context, *exec.Cmd) (release func(), err error)

func WithCommandPreparer(ctx context.Context, prepare CommandPreparer) context.Context {
	return context.WithValue(ctx, commandPreparerKey{}, prepare)
}

func Prepare(ctx context.Context, cmd *exec.Cmd) (func(), error) {
	if prepare, ok := ctx.Value(commandPreparerKey{}).(CommandPreparer); ok && prepare != nil {
		return prepare(ctx, cmd)
	}
	return func() {}, nil
}

func EnvironmentScope(ctx context.Context) Scope {
	scope, _ := ctx.Value(scopeKey{}).(Scope)
	return scope
}

func OutputContext(ctx context.Context, cmd *exec.Cmd) ([]byte, error) {
	release, err := Prepare(ctx, cmd)
	if err != nil {
		return nil, err
	}
	defer release()
	return Output(cmd)
}

func RunContext(ctx context.Context, cmd *exec.Cmd) error {
	release, err := Prepare(ctx, cmd)
	if err != nil {
		return err
	}
	defer release()
	return Result(cmd.Run())
}
