package builtins

import (
	"context"
	"errors"
	"io"
	stdfs "io/fs"

	"github.com/Lakeshore-Labs/gbash/commands"
	"github.com/Lakeshore-Labs/gbash/policy"
)

func readAllFile(ctx context.Context, inv *Invocation, name string) (data []byte, abs string, err error) {
	file, abs, err := openRead(ctx, inv, name)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = file.Close() }()

	data, err = readAllReader(ctx, inv, file)
	if err != nil {
		return nil, "", err
	}
	return data, abs, nil
}

func readAllStdin(ctx context.Context, inv *Invocation) ([]byte, error) {
	return commands.ReadAllStdin(ctx, inv)
}

func readAllReader(ctx context.Context, inv *Invocation, reader io.Reader) ([]byte, error) {
	return commands.ReadAll(ctx, inv, reader)
}

func readAllErrorText(err error) string {
	switch {
	case errorsIsNotExist(err):
		return "No such file or directory"
	case errorsIsDirectory(err):
		return "Is a directory"
	case errors.Is(err, stdfs.ErrPermission) && !policy.IsDenied(err):
		return "Permission denied"
	default:
		return err.Error()
	}
}
