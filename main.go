package main

import (
	"flag"
	"fmt"
	"io"
	"os"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		printUsage(stdout)
		return 0
	}

	switch args[0] {
	case "zip":
		return runZipCommand(args[1:], stdout, stderr)
	case "existing":
		return runExistingCommand(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "Unknown command %q\n\n", args[0])
		printUsage(stderr)
		return 2
	}
}

func printUsage(w io.Writer) {
	fmt.Fprintln(w, `Organize Google Photos Takeout files by date.

Usage:
  organize-photos <command> [options] SOURCE DEST

Commands:
  zip       Organize media directly from Takeout .zip files
  existing  Organize media in an already-extracted folder

Run "organize-photos <command> -h" for command options.`)
}

func commonFlags(fs *flag.FlagSet, opts *options) {
	fs.StringVar(&opts.structure, "structure", "year-month", "folder layout: year, year-month, or year-month-day")
	fs.BoolVar(&opts.utc, "utc", false, "bucket dates by UTC instead of local time")
	fs.BoolVar(&opts.dryRun, "dry-run", false, "show what would happen without writing or moving files")
}

func runZipCommand(args []string, stdout, stderr io.Writer) int {
	opts := options{}
	fs := flag.NewFlagSet("zip", flag.ContinueOnError)
	fs.SetOutput(stderr)
	commonFlags(fs, &opts)
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if fs.NArg() != 2 {
		fmt.Fprintln(stderr, "Usage: organize-photos zip [options] SOURCE DEST")
		fs.PrintDefaults()
		return 2
	}
	if !validStructure(opts.structure) {
		fmt.Fprintf(stderr, "Invalid structure %q (choose year, year-month, or year-month-day)\n", opts.structure)
		return 2
	}
	if err := organizeZips(fs.Arg(0), fs.Arg(1), opts, stdout, stderr); err != nil {
		fmt.Fprintf(stderr, "Error: %v\n", err)
		return 1
	}
	return 0
}

func runExistingCommand(args []string, stdout, stderr io.Writer) int {
	opts := options{}
	fs := flag.NewFlagSet("existing", flag.ContinueOnError)
	fs.SetOutput(stderr)
	commonFlags(fs, &opts)
	fs.BoolVar(&opts.copyFiles, "copy", false, "copy instead of move, leaving SOURCE untouched")
	fs.BoolVar(&opts.removeDuplicates, "remove-duplicates", false, "remove source files already present in DEST (ignored with --copy)")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if fs.NArg() != 2 {
		fmt.Fprintln(stderr, "Usage: organize-photos existing [options] SOURCE DEST")
		fs.PrintDefaults()
		return 2
	}
	if !validStructure(opts.structure) {
		fmt.Fprintf(stderr, "Invalid structure %q (choose year, year-month, or year-month-day)\n", opts.structure)
		return 2
	}
	if err := organizeExisting(fs.Arg(0), fs.Arg(1), opts, stdout, stderr); err != nil {
		fmt.Fprintf(stderr, "Error: %v\n", err)
		return 1
	}
	return 0
}
