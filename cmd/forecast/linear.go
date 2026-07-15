package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"forecasting/internal/linear"
	"forecasting/internal/sqlite"
	"forecasting/internal/syncer"
	"forecasting/internal/util"
)

func cmdLinearSync(args []string) error {
	cmd := flag.NewFlagSet("linear sync", flag.ExitOnError)
	dbFile := addDBFlag(cmd)
	teams := addTeamsFlag(cmd, "comma-separated team keys, e.g. ENG,DESIGN; limits the candidate team set")
	allTeams := cmd.Bool("all-teams", false, "expand the candidate team set to every accessible Linear team; mutually exclusive with -teams")
	fullReload := cmd.Bool("full-reload", false, "ignore each team's stored watermark and do a full reload")
	configFile := addConfigFlag(cmd)
	cmd.Parse(args)

	if err := util.ApplyConfig(cmd, *configFile); err != nil {
		return err
	}

	if err := requireDB(dbFile); err != nil {
		return err
	}

	apiKey, err := linear.GetAPIKey()
	if err != nil {
		return err
	}

	client := linear.New(apiKey)

	store, err := sqlite.Open(*dbFile)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer store.Close()

	return syncer.Run(context.Background(), client, store, syncer.Options{
		Teams:      *teams,
		AllTeams:   *allTeams,
		FullReload: *fullReload,
	})
}

func cmdLinearTeams(args []string) error {
	cmd := flag.NewFlagSet("linear teams", flag.ExitOnError)
	configFile := addConfigFlag(cmd)
	cmd.Parse(args)

	if err := util.ApplyConfig(cmd, *configFile); err != nil {
		return err
	}

	apiKey, err := linear.GetAPIKey()
	if err != nil {
		return err
	}

	client := linear.New(apiKey)
	teams, err := client.ListTeams(context.Background())
	if err != nil {
		return err
	}

	fmt.Fprintf(os.Stdout, "accessible teams (%d):\n", len(teams))
	for _, t := range teams {
		fmt.Fprintf(os.Stdout, "  %-12s %s\n", t.Key, t.Name)
	}
	return nil
}
