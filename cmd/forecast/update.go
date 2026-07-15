package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"forecasting/internal/logx"
	"forecasting/internal/selfupdate"

	"github.com/mattn/go-isatty"
)

func cmdUpdate(args []string) error {
	fs := flag.NewFlagSet("update", flag.ExitOnError)
	check := fs.Bool("check", false, "report current vs. latest version and exit without installing anything")
	yes := fs.Bool("yes", false, "skip the interactive confirmation prompt")
	force := fs.Bool("force", false, "proceed even if already on the latest version or the current version is unknown")
	timeout := fs.Duration("timeout", 60*time.Second, "overall HTTP timeout")
	if err := fs.Parse(args); err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	client := &http.Client{Timeout: *timeout}

	rel, err := selfupdate.LatestRelease(ctx, client)
	if err != nil {
		return fmt.Errorf("checking latest release: %w", err)
	}

	current := version
	if current == "" {
		logx.Warnf("current build has no version info (a dev build); can't compare against the latest release")
		if !*force {
			return fmt.Errorf("refusing to update a dev build without -force")
		}
	} else if selfupdate.SameVersion(current, rel.TagName) && !*force {
		fmt.Printf("already up to date (%s)\n", current)
		return nil
	}

	fmt.Printf("%s -> %s\n", orUnknown(current), rel.TagName)
	if *check {
		return nil
	}

	if !*yes {
		if !isatty.IsTerminal(os.Stdin.Fd()) {
			return fmt.Errorf("stdin is not a terminal; pass -yes to confirm the update non-interactively")
		}
		fmt.Fprintf(os.Stderr, "Update to %s? [y/N] ", rel.TagName)
		reader := bufio.NewReader(os.Stdin)
		line, _ := reader.ReadString('\n')
		line = strings.ToLower(strings.TrimSpace(line))
		if line != "y" && line != "yes" {
			return fmt.Errorf("update aborted")
		}
	}

	assetName := selfupdate.AssetName(rel.TagName)
	asset, ok := selfupdate.FindAsset(rel, assetName)
	if !ok {
		return fmt.Errorf("release %s has no asset %s", rel.TagName, assetName)
	}
	checksumsAsset, ok := selfupdate.FindAsset(rel, selfupdate.ChecksumsAssetName)
	if !ok {
		return fmt.Errorf("release %s has no %s", rel.TagName, selfupdate.ChecksumsAssetName)
	}

	checksumsData, err := selfupdate.Download(ctx, client, checksumsAsset.BrowserDownloadURL)
	if err != nil {
		return fmt.Errorf("downloading %s: %w", selfupdate.ChecksumsAssetName, err)
	}
	sums := selfupdate.ParseChecksums(checksumsData)
	wantHash, ok := sums[assetName]
	if !ok {
		return fmt.Errorf("%s does not list a checksum for %s", selfupdate.ChecksumsAssetName, assetName)
	}

	assetData, err := selfupdate.Download(ctx, client, asset.BrowserDownloadURL)
	if err != nil {
		return fmt.Errorf("downloading %s: %w", assetName, err)
	}
	if err := selfupdate.VerifySHA256(assetData, wantHash); err != nil {
		return fmt.Errorf("%s failed checksum verification: %w", assetName, err)
	}

	binName := selfupdate.BinaryNameInArchive()
	newBin, err := selfupdate.ExtractBinary(assetData, assetName, binName)
	if err != nil {
		return fmt.Errorf("extracting %s from %s: %w", binName, assetName, err)
	}

	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locating running executable: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}

	if err := selfupdate.ReplaceExecutable(exe, newBin); err != nil {
		return fmt.Errorf("installing update: %w", err)
	}

	fmt.Printf("updated to %s (%s)\n", rel.TagName, exe)
	return nil
}
