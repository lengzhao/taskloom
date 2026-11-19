// Command taskloom builds exactly two configuration layers and owns process signals.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	_ "github.com/lengzhao/taskloom/plugins"
	config "github.com/lengzhao/taskloom/runtime/config"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, out, errout io.Writer) int {
	flags := flag.NewFlagSet("taskloom", flag.ContinueOnError)
	flags.SetOutput(errout)
	base := flags.String("base", "base.yaml", "L0 base file")
	overlay := flags.String("config", "config.yaml", "L1 override file; empty path means no overlay")
	check := flags.Bool("check", false, "build typed plugin graph without opening resources or listeners")
	if err := flags.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(errout, "unexpected arguments; only one base and one config file are supported")
		return 2
	}
	app, result, err := config.BuildFiles(ctx, *base, *overlay)
	if err != nil {
		fmt.Fprintln(errout, err)
		return 1
	}
	if *check {
		fmt.Fprintf(out, "configuration valid: %d instances (construction only)\n", len(result.Instances))
		return 0
	}
	fmt.Fprintf(out, "starting taskloom: %d instances\n", len(result.Instances))
	if err := app.Run(ctx); err != nil {
		fmt.Fprintln(errout, err)
		return 1
	}
	fmt.Fprintln(out, "taskloom stopped")
	return 0
}
