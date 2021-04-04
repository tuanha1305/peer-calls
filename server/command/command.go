package command

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/juju/errors"
	"github.com/spf13/pflag"
)

var ErrCommandNotFound = errors.New("command not found")

// Handler is command line handler.
type Handler interface {
	// Handle receives the context and the arguments leftover from parsing.
	// When the first return value is non-nil and the error is nil, the value
	// will be passed as an argument to the subcommand.
	Handle(ctx context.Context, args []string) error
}

// HandlerFunc defines a functional implementation of Handler.
type HandlerFunc func(ctx context.Context, args []string) error

// Handle implements Handler interface.
func (h HandlerFunc) Handle(ctx context.Context, args []string) error {
	return h(ctx, args)
}

// FlagRegistry contains optional methods for parsing CLI arguments.
type FlagRegistry interface {
	// RegisterFlags can be implemented to register custom flags.
	RegisterFlags(cmd *Command, flags *pflag.FlagSet)
}

// FlagRegistryFunc defines a functional implementation of FlagRegistry.
type FlagRegistryFunc func(cmd *Command, flags *pflag.FlagSet)

// FlagRegistryFunc implements FlagRegistry.
func (f FlagRegistryFunc) RegisterFlags(cmd *Command, flags *pflag.FlagSet) {
	f(cmd, flags)
}

type ArgsProcessor interface {
	ProcessArgs(c *Command, args []string) []string
}

// ArgsProcessorFunc defines a functional implementation of ArgsProcessor.
type ArgsProcessorFunc func(cmd *Command, args []string) []string

// ArgsProcessorFunc implements ArgsProcessor.
func (f ArgsProcessorFunc) ProcessArgs(cmd *Command, args []string) []string {
	return f(cmd, args)
}

type Command struct {
	params      Params
	subCommands map[string]*Command
}

type Params struct {
	Name              string
	Desc              string
	ArgsPreProcessor  ArgsProcessor
	ArgsPostProcessor ArgsProcessor
	FlagRegistry      FlagRegistry
	Handler           Handler
	SubCommands       []*Command
}

func New(params Params) *Command {
	var subCommands map[string]*Command

	if len(params.SubCommands) > 0 {
		subCommands = make(map[string]*Command, len(params.SubCommands))

		for _, cmd := range params.SubCommands {
			subCommands[cmd.Name()] = cmd
		}
	}

	return &Command{
		params:      params,
		subCommands: subCommands,
	}
}

func (c Command) Name() string {
	return c.params.Name
}

func (c *Command) Exec(ctx context.Context, args []string) error {
	doneCh := make(chan struct{})
	defer func() {
		<-doneCh
	}()

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Register a channel for interrupts so we can cancel the above context.
	interruptCh := make(chan os.Signal, 1)
	signal.Notify(interruptCh, syscall.SIGINT, syscall.SIGTERM)

	// Start a goroutine and wait for the termination signals so the context
	// can be cancelled.
	go func() {
		defer close(doneCh)
		defer signal.Stop(interruptCh)

		select {
		case <-ctx.Done():
		case <-interruptCh:
			cancel()
		}
	}()

	flags := pflag.NewFlagSet(c.Name(), pflag.ContinueOnError)

	if c.params.ArgsPreProcessor != nil {
		args = c.params.ArgsPreProcessor.ProcessArgs(c, args)
	}

	// Need to set this to allow easier processing of subcommands.
	flags.SetInterspersed(false)

	if c.params.FlagRegistry != nil {
		c.params.FlagRegistry.RegisterFlags(c, flags)
	}

	err := flags.Parse(args)
	if err != nil {
		return errors.Annotatef(err, "parse args for command: %s", c.params.Name)
	}

	args = flags.Args()

	if c.params.Handler != nil {
		err = c.params.Handler.Handle(ctx, args)
		if err != nil {
			return errors.Trace(err)
		}
	}

	if c.params.ArgsPostProcessor != nil {
		args = c.params.ArgsPostProcessor.ProcessArgs(c, args)
	}

	if len(args) > 0 && args[0] == "--" {
		args = args[1:]
	}

	if len(args) > 0 && len(c.subCommands) > 0 {
		subName := args[0]
		subCommand, ok := c.subCommands[subName]
		if !ok {
			return errors.Annotatef(ErrCommandNotFound, "command: %s", subName)
		}

		err := subCommand.Exec(ctx, args[1:])
		if err != nil {
			return errors.Trace(err)
		}
	}

	return nil
}
