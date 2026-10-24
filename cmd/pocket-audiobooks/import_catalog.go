package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/abjelosevic88/pocket-homelab-audiobooks/internal/library"
)

func runCatalogImport(args []string) {
	flags := flag.NewFlagSet("import-audiobookshelf", flag.ExitOnError)
	source := flags.String("source", "", "consistent Audiobookshelf SQLite snapshot (opened read-only)")
	mounts := flags.String("mounts", "", "JSON map of source container roots to local media roots")
	users := flags.String("users", "", "JSON map of source account IDs to existing Pocket user IDs")
	apply := flags.Bool("apply", false, "commit validated import; default validates then rolls back")
	_ = flags.Parse(args)
	if *source == "" || *mounts == "" || *users == "" || flags.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "usage: pocket-audiobooks import-audiobookshelf --source snapshot.sqlite --mounts mounts.json --users users.json [--apply]")
		os.Exit(2)
	}
	opt := library.ImportOptions{Source: *source, Apply: *apply}
	if err := library.ReadJSONFile(*mounts, &opt.Mounts); err != nil {
		fmt.Fprintln(os.Stderr, "read mount map:", err)
		os.Exit(1)
	}
	if err := library.ReadJSONFile(*users, &opt.Users); err != nil {
		fmt.Fprintln(os.Stderr, "read account map:", err)
		os.Exit(1)
	}
	_, _, st := mustLoad()
	defer st.Close()
	report, err := (&library.Catalog{DB: st.DB}).Import(context.Background(), opt)
	if err != nil {
		fmt.Fprintln(os.Stderr, "import failed:", err)
		os.Exit(1)
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	_ = encoder.Encode(report)
}
