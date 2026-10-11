package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/envoryx/envoryx/internal/config"
	"github.com/envoryx/envoryx/internal/db"
	"github.com/envoryx/envoryx/internal/docker"
	"github.com/envoryx/envoryx/internal/instance"
	"github.com/envoryx/envoryx/internal/selfupdate"
)

// selfUpdateHelper is the helper container of a self-update (internal/selfupdate): it
// replaces the Envoryx container with one of the new image and exits.
func selfUpdateHelper(args []string) int {
	fs := flag.NewFlagSet("self-update", flag.ContinueOnError)
	container := fs.String("container", "", "the Envoryx container to replace")
	image := fs.String("image", "", "the image of the new version")
	if err := fs.Parse(args); err != nil || *container == "" || *image == "" {
		fmt.Fprintln(os.Stderr, "usage: envoryx self-update --container <id> --image <ref> (started by Envoryx itself)")
		return 2
	}
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "envoryx self-update: configuration:", err)
		return 1
	}
	log := newLogger(cfg)
	// The helper outlives nothing: the old container's stop must not end it.
	signal.Ignore(syscall.SIGTERM, syscall.SIGINT)
	engine, err := docker.Connect(docker.Options{Host: cfg.DockerHost}, log)
	if err != nil {
		log.Error("update: docker client", "err", err)
		return 1
	}
	defer engine.Close()
	backups := &instance.Store{ConfigDir: cfg.ConfigDir, DBPath: cfg.DatabasePath, Dir: filepath.Join(cfg.BackupsDir, "_instance"),
		Version: version, LatestSchema: db.LatestVersion(), Log: log}
	h := &selfupdate.Helper{Engine: engine, ConfigDir: cfg.ConfigDir, Backups: backups, Log: log}
	if err := h.Run(context.Background(), *container, *image); err != nil {
		log.Error("update failed", "err", err)
		return 1
	}
	log.Info("update done", "image", *image)
	return 0
}
