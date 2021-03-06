package cmd

import (
	"fmt"
	"github.com/go-ap/auth"
	"github.com/go-ap/errors"
	"github.com/go-ap/fedbox/internal/config"
	"github.com/go-ap/fedbox/internal/env"
	"github.com/go-ap/fedbox/storage/boltdb"
	"github.com/go-ap/fedbox/storage/pgx"
	"golang.org/x/crypto/ssh/terminal"
	"os"
	"path"
)

func (c *Control) Bootstrap(dir string, typ config.StorageType, environ env.Type) error {
	if typ == config.BoltDB {
		storagePath := config.GetBoltDBPath(dir, c.Host, environ)
		err := boltdb.Bootstrap(storagePath, c.BaseURL)
		if err != nil {
			return errors.Annotatef(err, "Unable to create %s db", storagePath)
		}
		oauthPath := config.GetBoltDBPath(dir, fmt.Sprintf("%s-oauth", c.Host), environ)
		if _, err := os.Stat(oauthPath); os.IsNotExist(err) {
			err := auth.BootstrapBoltDB(oauthPath, []byte(c.Host))
			if err != nil {
				return errors.Annotatef(err, "Unable to create %s db", oauthPath)
			}
		}
	}
	var pgRoot string
	if typ == config.Postgres {
		// ask for root pw
		fmt.Printf("%s password: ", pgRoot)
		pgPw, _ := terminal.ReadPassword(0)
		fmt.Println()
		dir, _ := os.Getwd()
		path := path.Join(dir, "init.sql")
		err := pgx.Bootstrap(c.Conf, pgRoot, pgPw, path)
		if err != nil {
			return errors.Annotatef(err, "Unable to update %s db", typ)
		}
	}
	return nil
}

func (c *Control) BootstrapReset(dir string, typ config.StorageType, environ env.Type) error {
	if typ == config.BoltDB {
		path := config.GetBoltDBPath(dir, c.Host, environ)
		err := boltdb.Clean(path)
		if err != nil {
			return errors.Annotatef(err, "Unable to update %s db", typ)
		}
	}
	var pgRoot string
	if typ == config.Postgres {
		// ask for root pw
		fmt.Printf("%s password: ", pgRoot)
		pgPw, _ := terminal.ReadPassword(0)
		fmt.Println()
		dir, _ := os.Getwd()
		path := path.Join(dir, "init.sql")
		err := pgx.Clean(c.Conf, pgRoot, pgPw, path)
		if err != nil {
			return errors.Annotatef(err, "Unable to update %s db", typ)
		}
	}
	return nil
}
