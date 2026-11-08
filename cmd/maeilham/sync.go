package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/maeilham/server/internal/content"
)

type SyncCmd struct {
	Repo string `short:"r" help:"sync할 repo slug (미지정 시 전체 active repo)"`
}

func (c *SyncCmd) Run(ctx context.Context, d *deps) error {
	return syncRepos(ctx, d, c.Repo)
}

// syncRepos는 slug가 비어 있으면 전체 active repo를 sync한다. 실패한 repo가 있으면 에러를 돌려준다.
func syncRepos(ctx context.Context, d *deps, slug string) error {
	repos, err := d.repoStore.ListActive(ctx)
	if err != nil {
		return err
	}
	if slug != "" {
		filtered := repos[:0]
		for _, r := range repos {
			if r.Slug == slug {
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

	var errs []error
	for _, r := range repos {
		log := d.log.With("repo", r.Slug)
		log.Info("sync starting", "url", r.GitHubURL)
		stats, err := content.Sync(ctx, log, d.contentStore, ghClient, ghApp, r.Slug, r.GitHubURL, "")
		if err != nil {
			log.Error("sync", "err", err)
			errs = append(errs, fmt.Errorf("sync %s: %w", r.Slug, err))
			continue
		}
		if ghApp != nil {
			created, failed, err := content.EnsureDiscussions(ctx, log, ghApp, d.contentStore, r)
			if err != nil {
				log.Error("ensure discussions", "err", err)
			} else if created > 0 || failed > 0 {
				log.Info("discussions", "created", created, "failed", failed)
			}
		}
		log.Info("sync done",
			"scanned", stats.Scanned, "inserted", stats.Inserted,
			"updated", stats.Updated, "deleted", stats.Deleted,
			"skipped", stats.Skipped, "errors", stats.Errors,
			"authored", stats.Authored, "authored_failed", stats.AuthoredFailed)
	}
	return errors.Join(errs...)
}
