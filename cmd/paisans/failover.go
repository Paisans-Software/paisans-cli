package main

import (
	"flag"
	"fmt"
	"github.com/paisans-software/paisans-cli/internal/config"

	"github.com/paisans-software/paisans-cli/internal/failover"
)

func runFailover(args []string) error {
	if len(args) < 1 || args[0] != "test" {
		return fmt.Errorf("failover takes one subcommand, test")
	}
	return runFailoverTest(args[1:])
}

// runFailoverTest moves the primary to another data site and back. It is a
// dry run by default like every command that reaches a host, and more
// deliberately so than most: each switch interrupts writes for everyone.
func runFailoverTest(args []string) error {
	fs := newFlagSet("failover test", flag.ExitOnError)
	reporter := commonFlags(fs)
	configPath := fs.String("config", config.FileName, "path to the deployment declaration")
	execute := fs.Bool("execute", false, "actually switch the primary over and back")
	if err := fs.Parse(args); err != nil {
		return err
	}
	r := reporter()
	cfg, err := loadChecked(r, *configPath)
	if err != nil {
		return err
	}
	// The switchover runs on the data sites, and the restart after it on
	// the apps sites.
	if err := claimSites(r, cfg, *execute, union(cfg.Cluster.Sites, cfg.AppsSites())...); err != nil {
		return err
	}
	return failover.Run(cfg, failover.Options{
		Transports: allSiteTransports(cfg),
		Report:     r,
		Execute:    *execute,
	})
}
