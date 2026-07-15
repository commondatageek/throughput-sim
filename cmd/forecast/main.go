package main

import (
	"fmt"
	"os"
)

type command struct {
	Name    string
	Summary string
	Run     func(args []string) error
}

var simCommands = []command{
	{Name: "items", Summary: "How many items can N engineers complete in D days?", Run: cmdSimItems},
	{Name: "days", Summary: "How many days for N engineers to complete I items?", Run: cmdSimDays},
	{Name: "probability", Summary: "What is the probability of completing I items in D days?", Run: cmdSimProbability},
	{Name: "backtest", Summary: "Replay probability forecasts day-by-day for a project/milestone.", Run: cmdSimBacktest},
}

var linearCommands = []command{
	{Name: "sync", Summary: "Sync issues from Linear into the database.", Run: cmdLinearSync},
	{Name: "teams", Summary: "List accessible Linear teams.", Run: cmdLinearTeams},
}

var topCommands = []command{
	{Name: "aging", Summary: "WIP-age and cycle-time report.", Run: cmdAging},
	{Name: "cfd", Summary: "Cumulative flow diagram.", Run: cmdCFD},
	{Name: "count", Summary: "Count of non-terminal issues, grouped by project.", Run: cmdCount},
	{Name: "version", Summary: "Print version and build info.", Run: cmdVersion},
}

func usage() {
	fmt.Fprintln(os.Stderr, "Usage: forecast <command> [flags]")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "Commands:")
	for _, c := range topCommands {
		fmt.Fprintf(os.Stderr, "  %-22s %s\n", c.Name, c.Summary)
	}
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "  sim <subcommand>       Monte Carlo simulation engine.")
	for _, c := range simCommands {
		fmt.Fprintf(os.Stderr, "    %-20s %s\n", c.Name, c.Summary)
	}
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "  linear <subcommand>    Linear-specific ingest helpers.")
	for _, c := range linearCommands {
		fmt.Fprintf(os.Stderr, "    %-20s %s\n", c.Name, c.Summary)
	}
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "Run 'forecast <command> -help' for command-specific flags.")
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}

	var err error
	switch os.Args[1] {
	case "sim":
		err = runGroup("sim", simCommands, os.Args[2:])
	case "linear":
		err = runGroup("linear", linearCommands, os.Args[2:])
	default:
		for _, c := range topCommands {
			if c.Name == os.Args[1] {
				if err := c.Run(os.Args[2:]); err != nil {
					fmt.Fprintf(os.Stderr, "error: %v\n", err)
					os.Exit(1)
				}
				return
			}
		}
		fmt.Fprintf(os.Stderr, "unknown command: %q\n\n", os.Args[1])
		usage()
		os.Exit(1)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func runGroup(group string, cmds []command, args []string) error {
	if len(args) == 0 {
		fmt.Fprintf(os.Stderr, "Usage: forecast %s <subcommand> [flags]\n\nSubcommands:\n", group)
		for _, c := range cmds {
			fmt.Fprintf(os.Stderr, "  %-20s %s\n", c.Name, c.Summary)
		}
		os.Exit(1)
	}
	for _, c := range cmds {
		if c.Name == args[0] {
			return c.Run(args[1:])
		}
	}
	fmt.Fprintf(os.Stderr, "unknown subcommand %q\n\nRun 'forecast %s' for available subcommands.\n", args[0], group)
	os.Exit(1)
	return nil
}
