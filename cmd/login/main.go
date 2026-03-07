package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"brainhub/internal/oauth"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	switch os.Args[1] {
	case "login":
		if code := runLogin(os.Args[2:]); code != 0 {
			os.Exit(code)
		}
	default:
		usage()
		os.Exit(2)
	}
}

func runLogin(args []string) int {
	fs := flag.NewFlagSet("login", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	noBrowser := fs.Bool("no-browser", false, "Do not open the browser automatically")
	verbose := fs.Bool("verbose", false, "Enable verbose logging")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	code, err := oauth.RunLogin(context.Background(), oauth.LoginOptions{
		NoBrowser: *noBrowser,
		Verbose:   *verbose,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "ERROR:", err)
		if code == 0 {
			return 1
		}
	}
	return code
}

func usage() {
	fmt.Fprintln(os.Stderr, "Usage:")
	fmt.Fprintln(os.Stderr, "  chatmock login [--no-browser] [--verbose]")
}
