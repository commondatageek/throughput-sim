package main

import (
	"flag"
	"fmt"
)

func cmdVersion(args []string) error {
	fs := flag.NewFlagSet("version", flag.ExitOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}

	v := version
	if v == "" {
		v = "(dev)"
	}

	bi := buildInfo()
	fmt.Printf("version:    %s\n", v)
	fmt.Printf("git sha:    %s\n", orUnknown(bi.GitSHA))
	fmt.Printf("git time:   %s\n", orUnknown(bi.GitTime))
	fmt.Printf("dirty:      %v\n", bi.Dirty)
	fmt.Printf("go version: %s\n", orUnknown(bi.GoVersion))
	fmt.Printf("module:     %s\n", orUnknown(bi.Module))
	return nil
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}
