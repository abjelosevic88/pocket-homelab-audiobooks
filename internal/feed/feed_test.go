package feed

import (
	"strings"
	"testing"
)

func TestRSSAtomAndOPML(t *testing.T) {
	rss := []byte(`<?xml version="1.0"?><rss version="2.0" xmlns:itunes="http://www.itunes.com/dtds/podcast-1.0.dtd"><channel><title>Show &amp; More</title><itunes:author>A host</itunes:author><itunes:explicit>true</itunes:explicit><itunes:image href="/cover.png"/><item><guid>one</guid><title>Episode 1</title><enclosure url="audio.mp3" type="audio/mpeg" length="123"/><pubDate>Fri, 23 Oct 2026 12:00:00 +0000</pubDate><itunes:duration>01:02:03</itunes:duration></item><item><guid>one</guid><title>Duplicate</title><enclosure url="audio.mp3"/></item><item><guid>unsafe</guid><enclosure url="file:///etc/passwd"/></item></channel></rss>`)
	f, err := Parse(rss, "https://example.com/feed.xml")
	if err != nil || f.Title != "Show & More" || f.Author != "A host" || !f.Explicit || len(f.Episodes) != 1 {
		t.Fatalf("RSS %+v %v", f, err)
	}
	if f.Episodes[0].Duration != 3723 || f.Episodes[0].URL != "https://example.com/audio.mp3" || f.Image != "https://example.com/cover.png" {
		t.Fatal(f)
	}
	other, _ := Parse(rss, "https://different.example/feed.xml")
	if other.Episodes[0].ID == f.Episodes[0].ID {
		t.Fatal("GUID collision across shows")
	}
	atom := []byte(`<feed xmlns="http://www.w3.org/2005/Atom"><title>Atom</title><author><name>Host</name></author><entry><id>urn:one</id><title>One</title><published>2026-10-23T00:00:00Z</published><link rel="enclosure" href="/one.ogg" type="audio/ogg"/></entry></feed>`)
	a, err := Parse(atom, "https://example.com/atom")
	if err != nil || len(a.Episodes) != 1 || a.Episodes[0].PublishedAt == 0 {
		t.Fatalf("Atom %+v %v", a, err)
	}
	opml, err := ExportOPML([]Subscription{{Title: f.Title, URL: "https://example.com/rss?a=1&b=2"}})
	if err != nil {
		t.Fatal(err)
	}
	subs, err := OPML(opml)
	if err != nil || len(subs) != 1 || subs[0].Title != f.Title || !strings.Contains(subs[0].URL, "&b=2") {
		t.Fatalf("OPML %+v %v", subs, err)
	}
	if _, err = Parse([]byte(`<!DOCTYPE rss [<!ENTITY x SYSTEM "file:///etc/passwd">]><rss><channel><title>&x;</title></channel></rss>`), "https://example.com"); err == nil {
		t.Fatal("external entity accepted")
	}
	if duration("NaN") != 0 || duration("Inf") != 0 {
		t.Fatal("non-finite duration")
	}
}
