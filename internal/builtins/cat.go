package builtins

import (
	"context"
	"errors"
	"fmt"
	"io"
	stdfs "io/fs"
	"os"
	"strings"
	"syscall"

	"github.com/ewhauser/gbash/internal/commandutil"
)

type Cat struct{}

type catNumberingMode int

const (
	catNumberNone catNumberingMode = iota
	catNumberNonBlank
	catNumberAll
)

type catOptions struct {
	number       catNumberingMode
	squeezeBlank bool
	showTabs     bool
	showEnds     bool
	showNonprint bool
}

type catOutputState struct {
	lineNumber            int
	atLineStart           bool
	pendingCarriageReturn bool
	oneBlankKept          bool
}

type catInput struct {
	data   []byte
	reader io.Reader
	label  string
	close  func()
}

type catRedirectHandle interface {
	commandutil.RedirectMetadata
	Stat() (stdfs.FileInfo, error)
}

type catHostHandle interface {
	Stat() (stdfs.FileInfo, error)
	Seek(offset int64, whence int) (int64, error)
	Fd() uintptr
}

type catStatHandle interface {
	Stat() (stdfs.FileInfo, error)
}

type catUnderlyingReader interface {
	UnderlyingReader() io.Reader
}

type catUnderlyingWriter interface {
	UnderlyingWriter() io.Writer
}

func NewCat() *Cat {
	return &Cat{}
}

func (c *Cat) Name() string {
	return "cat"
}

func (c *Cat) Run(ctx context.Context, inv *Invocation) error {
	return RunCommand(ctx, c, inv)
}

func (c *Cat) Spec() CommandSpec {
	return CommandSpec{
		Name:  "cat",
		Usage: "cat [OPTION]... [FILE]...",
		Options: []OptionSpec{
			{Name: "show-all", Short: 'A', Long: "show-all", Help: "equivalent to -vET"},
			{Name: "number-nonblank", Short: 'b', Long: "number-nonblank", Help: "number nonempty output lines"},
			{Name: "show-nonprinting-ends", Short: 'e', Help: "equivalent to -vE"},
			{Name: "show-ends", Short: 'E', Long: "show-ends", Help: "display $ at end of each line"},
			{Name: "number", Short: 'n', Long: "number", Help: "number all output lines"},
			{Name: "squeeze-blank", Short: 's', Long: "squeeze-blank", Help: "suppress repeated empty output lines"},
			{Name: "show-nonprinting-tabs", Short: 't', Help: "equivalent to -vT"},
			{Name: "show-tabs", Short: 'T', Long: "show-tabs", Help: "display TAB characters as ^I"},
			{Name: "ignored-u", Short: 'u', Help: "ignored"},
			{Name: "show-nonprinting", Short: 'v', Long: "show-nonprinting", Help: "use ^ and M- notation, except for LFD and TAB"},
			{Name: "help", Long: "help", Help: "display this help and exit"},
			{Name: "version", Long: "version", Help: "output version information and exit"},
		},
		Args: []ArgSpec{
			{Name: "file", ValueName: "FILE", Repeatable: true},
		},
		Parse: ParseConfig{
			InferLongOptions:      true,
			GroupShortOptions:     true,
			LongOptionValueEquals: true,
		},
		HelpRenderer:    renderStaticHelp(catHelpText),
		VersionRenderer: renderStaticVersion(catVersionText),
	}
}

func (c *Cat) RunParsed(ctx context.Context, inv *Invocation, matches *ParsedCommand) error {
	if matches.Has("help") {
		return renderStaticHelp(catHelpText)(inv.Stdout, c.Spec())
	}
	if matches.Has("version") {
		return renderStaticVersion(catVersionText)(inv.Stdout, c.Spec())
	}
	opts := catOptionsFromParsed(matches)
	names := matches.Args("file")
	if len(names) == 0 {
		names = []string{"-"}
	}

	state := catOutputState{
		lineNumber:  1,
		atLineStart: true,
	}
	var firstFailure string
	fail := func(message string) {
		_, _ = fmt.Fprintf(inv.Stderr, "cat: %s\n", message)
		if firstFailure == "" {
			firstFailure = message
		}
	}
	// A canceled command ends the loop: what failed is the cancel, not an input
	// or the output, so nothing is reported for it.
	for _, name := range names {
		if ctx.Err() != nil {
			break
		}
		if catWouldUnsafeOverwrite(inv, name) {
			fail(name + ": input file is output file")
			continue
		}

		input, err := readCatInput(ctx, inv, name)
		if ctx.Err() != nil {
			if input.close != nil {
				input.close()
			}
			break
		}
		if err != nil {
			if code, ok := ExitCode(err); ok && code == 126 {
				return err
			}
			fail(catErrorMessage(name, catInputLabel(input, name), err))
			continue
		}
		err = writeCatInput(inv.Stdout, input, opts, &state)
		if input.close != nil {
			input.close()
		}
		if ctx.Err() != nil {
			break
		}
		var readErr catReadError
		if errors.As(err, &readErr) {
			fail(catErrorMessage(name, catInputLabel(input, name), readErr.err))
			continue
		}
		if err != nil {
			return catWriteExitError(inv, err)
		}
	}

	if state.pendingCarriageReturn {
		if opts.showNonprint {
			if _, err := io.WriteString(inv.Stdout, "^M"); err != nil && ctx.Err() == nil {
				return catWriteExitError(inv, err)
			}
		} else {
			if _, err := inv.Stdout.Write([]byte{'\r'}); err != nil && ctx.Err() == nil {
				return catWriteExitError(inv, err)
			}
		}
	}

	if err := ctx.Err(); err != nil {
		return err
	}
	if firstFailure == "" {
		return nil
	}
	return &ExitError{Code: 1, Err: errors.New("cat: " + firstFailure)}
}

func catOptionsFromParsed(matches *ParsedCommand) catOptions {
	opts := catOptions{
		squeezeBlank: matches.Has("squeeze-blank"),
		showTabs:     matches.Has("show-all") || matches.Has("show-tabs") || matches.Has("show-nonprinting-tabs"),
		showEnds:     matches.Has("show-all") || matches.Has("show-ends") || matches.Has("show-nonprinting-ends"),
		showNonprint: matches.Has("show-all") || matches.Has("show-nonprinting") || matches.Has("show-nonprinting-ends") || matches.Has("show-nonprinting-tabs"),
	}
	if matches.Has("number-nonblank") {
		opts.number = catNumberNonBlank
	} else if matches.Has("number") {
		opts.number = catNumberAll
	}
	return opts
}

// catWouldUnsafeOverwrite reports, before any input is opened, whether name is
// the path of the shell redirect cat writes to, or whether stdin is the file it
// writes to. Every named input is checked again once open, by catIsOutputFile.
func catWouldUnsafeOverwrite(inv *Invocation, name string) bool {
	outputWriter := catResolveWriter(inv.Stdout)
	output, ok := outputWriter.(catRedirectHandle)
	if ok && output.RedirectPath() != "" {
		if name == "-" {
			input, ok := catResolveReader(inv.Stdin).(catRedirectHandle)
			if !ok || input.RedirectPath() == "" {
				return false
			}
			if input.RedirectPath() != output.RedirectPath() && !catSameFile(input, output) {
				return false
			}
			return catIsUnsafeOverwrite(input.RedirectOffset(), output)
		}
		return allowPath(inv, name) == output.RedirectPath() && catIsUnsafeOverwrite(0, output)
	}
	if name != "-" {
		return false
	}
	outputHost, ok := outputWriter.(catHostHandle)
	if !ok {
		return false
	}
	inputHost, ok := catResolveReader(inv.Stdin).(catHostHandle)
	return ok && catIsUnsafeHostOverwrite(inputHost, outputHost)
}

// catIsOutputFile reports whether the opened input, described by info, is the
// file cat writes to, with unread output still ahead of it. Like GNU cat it
// compares the open file, not its path, so a hard link to the output counts.
func catIsOutputFile(inv *Invocation, info stdfs.FileInfo) bool {
	if info == nil || !info.Mode().IsRegular() {
		return false
	}
	outputWriter := catResolveWriter(inv.Stdout)
	if output, ok := outputWriter.(catRedirectHandle); ok && output.RedirectPath() != "" {
		outputInfo, err := output.Stat()
		return err == nil && testSameFile(info, outputInfo) && catIsUnsafeOverwrite(0, output)
	}
	outputHost, ok := outputWriter.(catHostHandle)
	if !ok {
		return false
	}
	outputInfo, err := outputHost.Stat()
	if err != nil || !outputInfo.Mode().IsRegular() || !testSameFile(info, outputInfo) {
		return false
	}
	return catUnsafeByOffsets(0, outputInfo.Size(), catHostAppendMode(outputHost), catHostOffset(outputHost))
}

// catSameFile reports whether a and b are one file, such as a hard link and
// its target.
func catSameFile(a, b catStatHandle) bool {
	infoA, errA := a.Stat()
	infoB, errB := b.Stat()
	return errA == nil && errB == nil && testSameFile(infoA, infoB)
}

func catResolveReader(reader io.Reader) io.Reader {
	for reader != nil {
		underlying, ok := reader.(catUnderlyingReader)
		if !ok {
			return reader
		}
		next := underlying.UnderlyingReader()
		if next == nil || next == reader {
			return reader
		}
		reader = next
	}
	return nil
}

func catResolveWriter(writer io.Writer) io.Writer {
	for writer != nil {
		underlying, ok := writer.(catUnderlyingWriter)
		if !ok {
			return writer
		}
		next := underlying.UnderlyingWriter()
		if next == nil || next == writer {
			return writer
		}
		writer = next
	}
	return nil
}

func catIsUnsafeOverwrite(inputOffset int64, output catRedirectHandle) bool {
	info, err := output.Stat()
	if err != nil || info == nil || info.Size() == 0 {
		return false
	}
	return catUnsafeByOffsets(inputOffset, info.Size(), output.RedirectFlags()&os.O_APPEND != 0, output.RedirectOffset())
}

func catIsUnsafeHostOverwrite(input, output catHostHandle) bool {
	inputInfo, err := input.Stat()
	if err != nil {
		return false
	}
	outputInfo, err := output.Stat()
	if err != nil || !testSameFile(inputInfo, outputInfo) {
		return false
	}
	return catUnsafeByOffsets(catHostOffset(input), outputInfo.Size(), catHostAppendMode(output), catHostOffset(output))
}

func catUnsafeByOffsets(inputOffset, outputSize int64, appendMode bool, outputOffset int64) bool {
	if outputSize == 0 {
		return false
	}
	if appendMode {
		return inputOffset < outputSize
	}
	return inputOffset < outputOffset
}

func catHostOffset(file catHostHandle) int64 {
	offset, err := file.Seek(0, io.SeekCurrent)
	if err != nil || offset < 0 {
		return 0
	}
	return offset
}

func readCatInput(ctx context.Context, inv *Invocation, name string) (catInput, error) {
	if name == "-" {
		return readCatStdin(ctx, inv)
	}
	abs := allowPath(inv, name)
	file, openErr := inv.FS.Open(ctx, abs)
	if openErr != nil {
		if errors.Is(openErr, stdfs.ErrInvalid) {
			info, _, statErr := statPath(ctx, inv, name)
			if statErr == nil && info != nil && info.IsDir() {
				return catInput{label: abs}, errors.New("is a directory")
			}
		}
		return catInput{label: abs}, openErr
	}
	info, statErr := file.Stat()
	if statErr == nil && info != nil && info.IsDir() {
		_ = file.Close()
		return catInput{label: abs}, errors.New("is a directory")
	}
	if statErr == nil && catIsOutputFile(inv, info) {
		_ = file.Close()
		return catInput{label: abs}, errors.New("input file is output file")
	}
	shouldStream := statErr != nil || catShouldStreamInfo(info)
	if shouldStream {
		return catInput{
			reader: commandutil.ReaderWithContext(ctx, file),
			label:  abs,
			close: func() {
				_ = file.Close()
			},
		}, nil
	}
	data, err := readAllReader(ctx, inv, file)
	_ = file.Close()
	return catInput{data: data, label: abs}, err
}

func readCatStdin(ctx context.Context, inv *Invocation) (catInput, error) {
	reader := io.Reader(strings.NewReader(""))
	if inv != nil && inv.Stdin != nil {
		reader = inv.Stdin
	}
	if info, ok := catReaderInfo(reader); ok && !catShouldStreamInfo(info) {
		data, err := readAllReader(ctx, inv, reader)
		return catInput{data: data, label: "-"}, err
	}
	return catInput{
		reader: commandutil.ReaderWithContext(ctx, reader),
		label:  "-",
	}, nil
}

func catReaderInfo(reader io.Reader) (stdfs.FileInfo, bool) {
	handle, ok := reader.(catStatHandle)
	if !ok {
		return nil, false
	}
	info, err := handle.Stat()
	if err != nil || info == nil {
		return nil, false
	}
	return info, true
}

func catShouldStreamInfo(info stdfs.FileInfo) bool {
	return info == nil || !info.Mode().IsRegular()
}

func catInputLabel(input catInput, fallback string) string {
	if input.label != "" {
		return input.label
	}
	return fallback
}

func catErrorMessage(name, label string, err error) string {
	if err == nil {
		return name
	}
	message := readAllErrorText(err)
	for _, prefix := range []string{
		label + ": ",
		"open " + label + ": ",
		"stat " + label + ": ",
		name + ": ",
		"open " + name + ": ",
		"stat " + name + ": ",
	} {
		if prefix == ": " || prefix == "open : " || prefix == "stat : " {
			continue
		}
		message = strings.TrimPrefix(message, prefix)
	}
	return fmt.Sprintf("%s: %s", name, message)
}

func writeCatInput(w io.Writer, input catInput, opts catOptions, state *catOutputState) error {
	if input.reader != nil {
		return writeCatReader(w, input.reader, opts, state)
	}
	return writeCatData(w, input.data, opts, state)
}

func writeCatReader(w io.Writer, reader io.Reader, opts catOptions, state *catOutputState) error {
	buf := make([]byte, 32<<10)
	for {
		n, err := reader.Read(buf)
		if n > 0 {
			if writeErr := writeCatData(w, buf[:n], opts, state); writeErr != nil {
				return writeErr
			}
		}
		if err == nil {
			continue
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		return catReadError{err}
	}
}

// catReadError is a failure to read an input, which cat reports and moves past,
// as against a failure to write its output, which ends it.
type catReadError struct{ err error }

func (e catReadError) Error() string { return e.err.Error() }

func writeCatData(w io.Writer, data []byte, opts catOptions, state *catOutputState) error {
	if state == nil {
		state = &catOutputState{lineNumber: 1, atLineStart: true}
	}

	for _, b := range data {
		if state.pendingCarriageReturn {
			if b == '\n' {
				if opts.showEnds {
					if _, err := io.WriteString(w, "^M"); err != nil {
						return err
					}
				} else {
					if _, err := w.Write([]byte{'\r'}); err != nil {
						return err
					}
				}
				state.pendingCarriageReturn = false
				if err := writeCatNewLine(w, opts, state); err != nil {
					return err
				}
				continue
			}
			if opts.showNonprint {
				if _, err := io.WriteString(w, "^M"); err != nil {
					return err
				}
			} else {
				if _, err := w.Write([]byte{'\r'}); err != nil {
					return err
				}
			}
			state.pendingCarriageReturn = false
			state.atLineStart = false
		}

		if b == '\r' && !opts.showNonprint {
			state.pendingCarriageReturn = true
			continue
		}
		if b == '\n' {
			if err := writeCatNewLine(w, opts, state); err != nil {
				return err
			}
			continue
		}

		state.oneBlankKept = false
		if state.atLineStart && opts.number != catNumberNone {
			if err := writeCatLineNumber(w, state.lineNumber); err != nil {
				return err
			}
			state.lineNumber++
		}
		if err := writeCatByte(w, b, opts); err != nil {
			return err
		}
		state.atLineStart = false
	}
	return nil
}

func catWriteExitError(inv *Invocation, err error) error {
	if err == nil {
		return nil
	}
	if catBrokenPipe(err) {
		return &ExitError{Code: 141}
	}
	return exitf(inv, 1, "cat: write error: %s", shellWriteErrorText(err))
}

func catBrokenPipe(err error) bool {
	if errors.Is(err, io.ErrClosedPipe) || errors.Is(err, syscall.EPIPE) {
		return true
	}
	lower := strings.ToLower(err.Error())
	return strings.Contains(lower, "broken pipe") || strings.Contains(lower, "closed pipe")
}

func writeCatNewLine(w io.Writer, opts catOptions, state *catOutputState) error {
	if state == nil {
		return nil
	}
	if state.atLineStart {
		if opts.squeezeBlank && state.oneBlankKept {
			return nil
		}
		if opts.number == catNumberAll {
			if err := writeCatLineNumber(w, state.lineNumber); err != nil {
				return err
			}
			state.lineNumber++
		}
		state.oneBlankKept = true
	} else {
		state.oneBlankKept = false
	}
	if opts.showEnds {
		if _, err := io.WriteString(w, "$\n"); err != nil {
			return err
		}
	} else {
		if _, err := w.Write([]byte{'\n'}); err != nil {
			return err
		}
	}
	state.atLineStart = true
	return nil
}

func writeCatLineNumber(w io.Writer, lineNumber int) error {
	_, err := fmt.Fprintf(w, "%6d\t", lineNumber)
	return err
}

func writeCatByte(w io.Writer, b byte, opts catOptions) error {
	if opts.showNonprint {
		switch {
		case b == '\t':
			if opts.showTabs {
				_, err := io.WriteString(w, "^I")
				return err
			}
			_, err := w.Write([]byte{'\t'})
			return err
		case b <= 8 || (b >= 10 && b <= 31):
			_, err := w.Write([]byte{'^', b + 64})
			return err
		case b >= 32 && b <= 126:
			_, err := w.Write([]byte{b})
			return err
		case b == 127:
			_, err := io.WriteString(w, "^?")
			return err
		case b >= 128 && b <= 159:
			_, err := w.Write([]byte{'M', '-', '^', b - 64})
			return err
		case b >= 160 && b <= 254:
			_, err := w.Write([]byte{'M', '-', b - 128})
			return err
		default:
			_, err := io.WriteString(w, "M-^?")
			return err
		}
	}
	if opts.showTabs && b == '\t' {
		_, err := io.WriteString(w, "^I")
		return err
	}
	_, err := w.Write([]byte{b})
	return err
}

const catHelpText = `Usage: cat [OPTION]... [FILE]...
Concatenate FILE(s) to standard output.

  -A, --show-all           equivalent to -vET
  -b, --number-nonblank    number nonempty output lines
  -e                       equivalent to -vE
  -E, --show-ends          display $ at end of each line
  -n, --number             number all output lines
  -s, --squeeze-blank      suppress repeated empty output lines
  -t                       equivalent to -vT
  -T, --show-tabs          display TAB characters as ^I
  -u                       ignored
  -v, --show-nonprinting   use ^ and M- notation, except for LFD and TAB
      --help               display this help and exit
      --version            output version information and exit
`

const catVersionText = "cat (gbash) dev\n"

var _ Command = (*Cat)(nil)
var _ SpecProvider = (*Cat)(nil)
var _ ParsedRunner = (*Cat)(nil)
