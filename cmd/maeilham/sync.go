package main

import (
	"context"

	"github.com/maeilham/server/internal/content"
)

type SyncCmd struct {
	Repo string `short:"r" help:"sync할 repo slug (미지정 시 전체 active repo)"`
}

func (c *SyncCmd) Run(ctx context.Context, d *deps) error {
	repos, err := d.repoStore.ListActive(ctx)
	if err != nil {
		return err
	}
	if c.Repo != "" {
		filtered := repos[:0]
		for _, r := range repos {
			if r.Slug == c.Repo {
				filtered = append(filtered, r)
			}
		}
		repos = filtered
	}
	if len(repos) == 0 {
		d.log.Warn("no repos to sync")
		return nil
	}

	ghClient := content.NewGitHubClient(d.cfg.GitHubToken)
	ghApp := d.ghApp()

	for _, r := range repos {
		log := d.log.With("repo", r.Slug)
		log.Info("sync starting", "url", r.GitHubURL)
		stats, err := content.Sync(ctx, log, d.contentStore, ghClient, ghApp, r.Slug, r.GitHubURL, "")
		if err != nil {
			log.Error("sync", "err", err)
			continue
		}
		log.Info("sync done",
			"scanned", stats.Scanned, "inserted", stats.Inserted,
			"updated", stats.Updated, "deleted", stats.Deleted,
			"skipped", stats.Skipped, "errors", stats.Errors)
	}
	return nil
}
