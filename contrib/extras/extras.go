// Package extras registers the stable opt-in contrib commands.
package extras

import (
	"fmt"

	"github.com/Lakeshore-Labs/gbash"
	"github.com/Lakeshore-Labs/gbash/commands"
	contribawk "github.com/Lakeshore-Labs/gbash/contrib/awk"
	contribhtmltomarkdown "github.com/Lakeshore-Labs/gbash/contrib/htmltomarkdown"
	contribjq "github.com/Lakeshore-Labs/gbash/contrib/jq"
	contribpython "github.com/Lakeshore-Labs/gbash/contrib/python"
	contribsqlite3 "github.com/Lakeshore-Labs/gbash/contrib/sqlite3"
	contribyq "github.com/Lakeshore-Labs/gbash/contrib/yq"
)

// FullRegistry returns the default registry plus the stable contrib commands.
func FullRegistry() *commands.Registry {
	registry := gbash.DefaultRegistry()
	if err := Register(registry); err != nil {
		panic(fmt.Sprintf("extras: register full registry: %v", err))
	}
	return registry
}

// Register adds every stable contrib command module to the registry.
func Register(registry commands.CommandRegistry) error {
	if registry == nil {
		return nil
	}
	if err := contribawk.Register(registry); err != nil {
		return err
	}
	if err := contribhtmltomarkdown.Register(registry); err != nil {
		return err
	}
	if err := contribjq.Register(registry); err != nil {
		return err
	}
	if err := contribpython.Register(registry); err != nil {
		return err
	}
	if err := contribsqlite3.Register(registry); err != nil {
		return err
	}
	return contribyq.Register(registry)
}
