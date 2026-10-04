package writefreely

import (
	"context"
	"time"

	"github.com/writeas/web-core/log"
)

type PostJob struct {
	ID     int64
	PostID string
	Action string
	Delay  int64
}

func addJob(app *App, p *PublicPost, action string, delay int64) error {
	j := &PostJob{
		PostID: p.ID,
		Action: action,
		Delay:  delay,
	}
	return app.db.InsertJob(j)
}

// Lock names for withJobLock, one per periodic job.
const (
	jobLockPublish     = "publish-jobs"
	jobLockOrphanSweep = "orphan-image-sweep"
)

// jobEmailPost sends the post a publish job is for. It is a variable so that
// tests can count sends without a mail provider.
var jobEmailPost = emailPost

// withJobLock runs job only if this process can take the cross-process lock
// called name (see dialect.TryJobLock), so that two app processes on one
// database do not both run the same tick. Only one should ever be running,
// but if a second is started by mistake, two publish runs would each email
// the same post to its subscribers. A run that cannot take the lock, or
// fails to, is skipped: the next tick tries again.
func withJobLock(app *App, name string, job func()) {
	unlock, ok, err := app.db.dialectOrDefault().TryJobLock(context.Background(), app.db.DB, name)
	if err != nil {
		log.Error("[jobs] Unable to take the %s lock: %v - Skipping.", name, err)
		return
	}
	if !ok {
		if debugging {
			log.Info("[jobs] Another process is running %s - Skipping.", name)
		}
		return
	}
	defer unlock()
	job()
}

// startOrphanImageSweep periodically removes uploaded images that were never
// attached to a post, which is what a draft that was abandoned after a drop
// leaves behind.
func startOrphanImageSweep(app *App) {
	t := time.NewTicker(1 * time.Hour)
	for {
		<-t.C
		app.refreshSettings(context.Background())
		if !app.Config().Uploads.Enabled {
			continue
		}
		withJobLock(app, jobLockOrphanSweep, func() {
			log.Info("[jobs] Sweeping orphaned image uploads...")
			sweepOrphanedImages(app)
		})
	}
}

func startPublishJobsQueue(app *App) {
	t := time.NewTicker(62 * time.Second)
	for {
		log.Info("[jobs] Done.")
		<-t.C
		withJobLock(app, jobLockPublish, func() { runPublishJobs(app) })
	}
}

// runPublishJobs is one tick of the publish jobs queue.
func runPublishJobs(app *App) {
	log.Info("[jobs] Fetching email publish jobs...")
	jobs, err := app.db.GetJobsToRun("email")
	if err != nil {
		log.Error("[jobs] %s - Skipping.", err)
		return
	}
	log.Info("[jobs] Running %d email publish jobs...", len(jobs))
	err = runJobs(app, jobs, true)
	if err != nil {
		log.Error("[jobs] Failed: %s", err)
	}
}

// runJobs sends each job's post. A job is claimed first (see ClaimJob), so
// another worker that selected the same job skips it instead of sending it
// again. A job that fails gives its claim back and stays queued for the next
// tick; one that succeeds is deleted while still claimed.
//
// A claim is never taken back on a timer. A worker that dies part way
// through a send leaves its job claimed, and so unsent rather than sent
// twice to the subscribers it had already reached.
func runJobs(app *App, jobs []*PostJob, reqColl bool) error {
	for _, j := range jobs {
		claimed, err := app.db.ClaimJob(j.ID)
		if err != nil {
			log.Error("[job #%d] Unable to claim: %s", j.ID, err)
			continue
		}
		if !claimed {
			log.Info("[job #%d] Claimed by another worker - Skipping.", j.ID)
			continue
		}
		release := func() {
			if err := app.db.ReleaseJob(j.ID); err != nil {
				log.Error("[job #%d] Unable to release claim: %s", j.ID, err)
			}
		}

		p, err := app.db.GetPost(j.PostID, 0)
		if err != nil {
			log.Info("[job #%d] Unable to get post: %s", j.ID, err)
			release()
			continue
		}
		if !p.CollectionID.Valid && reqColl {
			log.Info("[job #%d] Post %s not part of a collection", j.ID, p.ID)
			app.db.DeleteJob(j.ID)
			continue
		}
		coll, err := app.db.GetCollectionByID(p.CollectionID.Int64)
		if err != nil {
			log.Info("[job #%d] Unable to get collection: %s", j.ID, err)
			release()
			continue
		}
		coll.hostName = app.Config().App.Host
		coll.ForPublic()
		p.Collection = &CollectionObj{Collection: *coll}
		err = jobEmailPost(app, p, p.Collection.ID)
		if err != nil {
			log.Error("[job #%d] Failed to email post %s", j.ID, p.ID)
			release()
			continue
		}
		log.Info("[job #%d] Success for post %s.", j.ID, p.ID)
		app.db.DeleteJob(j.ID)
	}
	return nil
}
