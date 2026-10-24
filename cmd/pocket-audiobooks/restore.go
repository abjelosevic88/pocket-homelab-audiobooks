package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/abjelosevic88/pocket-homelab-audiobooks/internal/backup"
	"os"
)

func runBundleRestore(command string, args []string) {
	flags := flag.NewFlagSet(command, flag.ExitOnError)
	from := flags.String("from", "", "application backup bundle")
	to := flags.String("to", "", "new data directory (must not already exist)")
	_ = flags.Parse(args)
	if *from == "" || flags.NArg() != 0 || command == "restore" && *to == "" || command == "verify-backup" && *to != "" {
		fmt.Fprintln(os.Stderr, "specify --from BUNDLE and, for restore, --to NEW_DIRECTORY")
		os.Exit(2)
	}
	var m backup.Manifest
	var err error
	if command == "restore" {
		m, err = backup.Restore(context.Background(), *from, *to)
	} else {
		m, err = backup.Verify(context.Background(), *from)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, command+":", err)
		os.Exit(1)
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(m)
}
