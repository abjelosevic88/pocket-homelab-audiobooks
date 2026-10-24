package library

import (
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

var discFolder = regexp.MustCompile(`(?i)^(?:cd|disc|disk)[ ._-]*([0-9]+)$`)
var discFilename = regexp.MustCompile(`(?i)(?:^|[ ._-])(?:cd|disc|disk)[ ._-]*([0-9]+)(?:[ ._-]|$)`)

func discGroup(path, root string) string {
	dir := filepath.Dir(path)
	if discFolder.MatchString(filepath.Base(dir)) && filepath.Dir(dir) != root {
		return filepath.Dir(dir)
	}
	return dir
}
func positiveTag(value string) int {
	first, _, _ := strings.Cut(value, "/")
	n, _ := strconv.Atoi(strings.TrimSpace(first))
	if n < 0 {
		return 0
	}
	return n
}
func discOrder(f *scannedFile) (int, int) {
	disc := 0
	for _, m := range [][]string{discFolder.FindStringSubmatch(filepath.Base(filepath.Dir(f.Source))), discFilename.FindStringSubmatch(f.Name)} {
		if len(m) > 1 {
			disc = positiveTag(m[1])
			break
		}
	}
	tags := map[string]string{}
	if f.Probe != nil {
		tags = f.Probe.Tags
	} else {
		for k, v := range object(f.Doc["metaTags"]) {
			tags[strings.ToLower(k)] = str(v)
		}
	}
	if disc == 0 {
		for _, k := range []string{"disc", "discnumber", "disk", "tpos"} {
			if disc = positiveTag(tags[k]); disc > 0 {
				break
			}
		}
	}
	track := 0
	for _, k := range []string{"track", "tracknumber", "trck", "trk"} {
		if track = positiveTag(tags[k]); track > 0 {
			break
		}
	}
	return disc, track
}
