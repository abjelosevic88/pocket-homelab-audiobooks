package library

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

type PodcastSubscription struct {
	PodcastSettings
	LastCheck int64  `json:"last_check"`
	NextCheck int64  `json:"next_check"`
	Error     string `json:"error"`
	Actor     *int64 `json:"actor_id"`
}

func subscriptionRow(row scanner) (PodcastSubscription, error) {
	var p PodcastSubscription
	err := row.Scan(&p.Auto, &p.Interval, &p.Keep, &p.MaxNew, &p.Schedule, &p.Timezone, &p.LastCheck, &p.NextCheck, &p.Error, &p.Actor)
	return p, err
}

const podcastSettingsColumns = `auto_download,interval_seconds,keep_latest,max_new,schedule,timezone,last_check,next_check,error,actor_id`

func (c *Catalog) PodcastSubscription(ctx context.Context, uid int64, id string) (PodcastSubscription, error) {
	it, err := c.Item(ctx, uid, id)
	if err != nil {
		return PodcastSubscription{}, err
	}
	if it.MediaType != "podcast" {
		return PodcastSubscription{}, ErrNotFound
	}
	p, err := subscriptionRow(c.DB.QueryRowContext(ctx, `SELECT `+podcastSettingsColumns+` FROM catalog_podcast_subscriptions WHERE item_id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return PodcastSubscription{PodcastSettings: PodcastSettings{Interval: 3600, MaxNew: 3, Timezone: "UTC"}}, nil
	}
	return p, err
}

func completePodcastCheck(ctx context.Context, tx *sql.Tx, id string, now time.Time, failure string) error {
	p, err := subscriptionRow(tx.QueryRowContext(ctx, `SELECT `+podcastSettingsColumns+` FROM catalog_podcast_subscriptions WHERE item_id=?`, id))
	if err != nil {
		return err
	}
	next, err := NextPodcastCheck(p.PodcastSettings, now)
	if err != nil {
		return err
	}
	// Back off failed second-granularity schedules without losing the last success.
	if failure != "" && next.Before(now.Add(5*time.Minute)) {
		next = now.Add(5 * time.Minute)
	}
	last := p.LastCheck
	if failure == "" {
		last = now.UnixMilli()
	}
	_, err = tx.ExecContext(ctx, `UPDATE catalog_podcast_subscriptions SET download_watermark=CASE WHEN last_check=0 AND download_watermark=0 AND ?='' THEN ? ELSE download_watermark END,last_check=?,next_check=?,error=? WHERE item_id=?`, failure, last, last, next.UnixMilli(), failure, id)
	return err
}
func (c *Catalog) PodcastCheckFailed(ctx context.Context, id string) error {
	tx, err := c.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = completePodcastCheck(ctx, tx, id, time.Now(), "Feed check failed; try Check now"); err != nil {
		return err
	}
	return tx.Commit()
}
