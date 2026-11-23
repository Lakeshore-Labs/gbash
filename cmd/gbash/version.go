package main

import (
	"github.com/Lakeshore-Labs/gbash/cli"
)

var (
	version = "dev"
	commit  = "unknown"
	date    = ""
	builtBy = ""
)

func newCLIConfig() cli.Config {
	return cli.Config{
		Name: "gbash",
		Build: &cli.BuildInfo{
			Version: version,
			Commit:  commit,
			Date:    date,
			BuiltBy: builtBy,
		},
	}
}
