package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/abjelosevic88/pocket-homelab-audiobooks/internal/library"
)

func runCatalogImport(args []string) {
	flags := flag.NewFlagSet("import-audiobookshelf", flag.ExitOnError)
	source := flags.String("source", "", "consistent Audiobookshelf SQLite snapshot (opened read-only)")
	mounts := flags.String("mounts", "", "JSON map of source container roots to local media roots")
	users := flags.String("users", "", "JSON map of source account IDs to existing Pocket user IDs")
	exclude := flags.String("exclude-accounts", "", "comma-separated source account IDs to discard, including personal state and owned lists")
	podcastOwner := flags.String("podcast-owner", "", "retained source account ID used for global podcast subscription jobs")
	scheduleTimezone := flags.String("schedule-timezone", "", "source server IANA timezone for imported calendar schedules")
	apply := flags.Bool("apply", false, "commit validated import; default validates then rolls back")
	_ = flags.Parse(args)
	if *source == "" || *mounts == "" || *users == "" || flags.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "usage: pocket-audiobooks import-audiobookshelf --source snapshot.sqlite --mounts mounts.json --users users.json [--apply]")
		os.Exit(2)
	}
	opt := library.ImportOptions{Source: *source, Apply: *apply, PodcastOwner: *podcastOwner, ScheduleTimezone: *scheduleTimezone}
	if *exclude != "" {
		for _, id := range strings.Split(*exclude, ",") {
			opt.ExcludeAccounts = append(opt.ExcludeAccounts, strings.TrimSpace(id))
		}
	}
	if err := library.ReadJSONFile(*mounts, &opt.Mounts); err != nil {
		fmt.Fprintln(os.Stderr, "read mount map:", err)
		os.Exit(1)
	}
	if err := library.ReadJSONFile(*users, &opt.Users); err != nil {
		fmt.Fprintln(os.Stderr, "read account map:", err)
		os.Exit(1)
	}
	_, _, st, lease := mustLoadExclusive()
	defer lease.Close()
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
