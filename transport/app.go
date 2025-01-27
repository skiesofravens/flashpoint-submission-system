package transport

import (
	"context"
	"database/sql"
	"fmt"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kofalt/go-memoize"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/Dri0m/flashpoint-submission-system/config"
	"github.com/Dri0m/flashpoint-submission-system/logging"
	"github.com/Dri0m/flashpoint-submission-system/resumableuploadservice"
	"github.com/Dri0m/flashpoint-submission-system/service"
	"github.com/Dri0m/flashpoint-submission-system/utils"
	"github.com/bwmarrin/discordgo"
	"github.com/gorilla/mux"
	"github.com/gorilla/schema"
	"github.com/gorilla/securecookie"
	"github.com/sirupsen/logrus"
)

// App is App
type App struct {
	Conf                *config.Config
	CC                  utils.CookieCutter
	Service             *service.SiteService
	decoder             *schema.Decoder
	authMiddlewareCache *memoize.Memoizer
	DFStorage           *DeviceFlowStorage
}

func InitApp(l *logrus.Entry, conf *config.Config, db *sql.DB, pgdb *pgxpool.Pool, authBotSession, notificationBotSession *discordgo.Session, rsu *resumableuploadservice.ResumableUploadService) {
	l.Infoln("initializing the server")
	router := mux.NewRouter()
	srv := &http.Server{
		Addr:    fmt.Sprintf("0.0.0.0:%d", conf.Port),
		Handler: logging.LogRequestHandler(l, router),
	}

	decoder := schema.NewDecoder()
	decoder.ZeroEmpty(false)
	decoder.IgnoreUnknownKeys(true)

	a := &App{
		Conf: conf,
		CC: utils.CookieCutter{
			Previous: securecookie.New([]byte(conf.SecurecookieHashKeyPrevious), []byte(conf.SecurecookieBlockKeyPrevious)),
			Current:  securecookie.New([]byte(conf.SecurecookieHashKeyCurrent), []byte(conf.SecurecookieBlockKeyPrevious)),
		},
		DFStorage: NewDeviceFlowStorage(conf.DeviceFlowVerificaitonUrl),
		Service: service.New(l, db, pgdb, authBotSession, notificationBotSession, conf.FlashpointServerID,
			conf.NotificationChannelID, conf.CurationFeedChannelID, conf.ValidatorServerURL, conf.SessionExpirationSeconds,
			conf.SubmissionsDirFullPath, conf.SubmissionImagesDirFullPath, conf.FlashfreezeDirFullPath, conf.IsDev,
			rsu, conf.ArchiveIndexerServerURL, conf.FlashfreezeIngestDirFullPath,
			conf.DataPacksDir),
		decoder:             decoder,
		authMiddlewareCache: memoize.NewMemoizer(5*time.Second, 60*time.Minute),
	}

	l.WithField("port", conf.Port).Infoln("starting the server...")

	go func() {
		a.handleRequests(l, srv, router)
	}()

	ctx, cancelFunc := context.WithCancel(context.Background())

	wg := &sync.WaitGroup{}

	l.Infoln("starting the data pack indexer...")

	a.Service.DataPacksIndexer.Start()

	l.Infoln("starting the notification consumer...")

	wg.Add(1)
	go func() {
		a.Service.RunNotificationConsumer(l, ctx, wg)
	}()

	// disable memstats for now
	//l.Infoln("starting the memstats printer...")
	//wg.Add(1)
	//go memstatsPrinter(l, ctx, wg)

	term := make(chan os.Signal, 1)
	signal.Notify(term, os.Interrupt, syscall.SIGINT, syscall.SIGTERM)
	<-term
	l.Infoln("signal received")

	l.Infoln("waiting for all goroutines to finish...")
	cancelFunc()
	wg.Wait()

	l.Infoln("closing the auth bot session...")
	authBotSession.Close()

	l.Infoln("closing the notification bot session...")
	notificationBotSession.Close()

	l.Infoln("closing data pack indexer...")
	a.Service.DataPacksIndexer.Stop()

	l.Infoln("shutting down the server...")
	if err := srv.Shutdown(context.Background()); err != nil {
		l.WithError(err).Errorln("server shutdown failed")
	}

	l.Infoln("goodbye")
}

func memstatsPrinter(l *logrus.Entry, ctx context.Context, wg *sync.WaitGroup) {
	defer wg.Done()
	defer l.Infoln("memstats printer stopped")

	bucket, ticker := utils.NewBucketLimiter(60*time.Second, 1)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			l.Infoln("context cancelled, stopping memstats printer")
			return
		case <-bucket:
			m := utils.GetMemStats()
			l.WithFields(logrus.Fields{"alloc": m.Alloc, "sys": m.Sys, "num_gc": m.NumGC, "heap_objects": m.HeapObjects, "gc_cpu_fraction": fmt.Sprintf("%.6f", m.GCCPUFraction)}).Debug("memstats")
		}
	}
}
