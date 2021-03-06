package main

import (
	"bytes"
	"fmt"
	pub "github.com/go-ap/activitypub"
	"github.com/go-ap/auth"
	"github.com/go-ap/errors"
	"github.com/go-ap/fedbox/cmd"
	"github.com/go-ap/fedbox/internal/config"
	"github.com/go-ap/fedbox/internal/env"
	"github.com/go-ap/fedbox/storage/boltdb"
	"github.com/go-ap/fedbox/storage/pgx"
	"github.com/go-ap/storage"
	"github.com/openshift/osin"
	"github.com/sirupsen/logrus"
	"golang.org/x/crypto/ssh/terminal"
	"gopkg.in/urfave/cli.v2"
	"os"
	"strings"
)

func errf(s string, par ...interface{}) {
	fmt.Fprintf(os.Stderr, s, par...)
}

type pgFlags struct {
	host string
	port int64
	user string
	pw   []byte
}

type boltFlags struct {
	path string
	root string
}

type ctlFlags struct {
	env      env.Type
	dir      string
	typ      config.StorageType
	url      string
	postgres pgFlags
	bolt     boltFlags
}

func setup(c *cli.Context, l logrus.FieldLogger, o *cmd.Control) error {
	dir := c.String("dir")
	if dir == "" {
		dir = "."
	}
	environ := env.Type(c.String("env"))
	if environ == "" {
		environ = env.DEV
	}
	typ := config.StorageType(c.String("type"))
	if typ == "" {
		typ = config.BoltDB
	}
	conf, err := config.LoadFromEnv(environ)
	if err != nil {
		l.Errorf("Unable to load config files for environment %s: %s", environ, err)
	}

	host := conf.Host
	var aDb osin.Storage
	var db storage.Repository
	if typ == config.BoltDB {
		path := config.GetBoltDBPath(dir, fmt.Sprintf("%s-oauth", host), environ)
		aDb = auth.NewBoltDBStore(auth.BoltConfig{
			Path:       path,
			BucketName: host,
			LogFn:      func(f logrus.Fields, s string, p ...interface{}) { l.WithFields(f).Infof(s, p...) },
			ErrFn:      func(f logrus.Fields, s string, p ...interface{}) { l.WithFields(f).Errorf(s, p...) },
		})
		db = boltdb.New(boltdb.Config{
			Path:  config.GetBoltDBPath(dir, host, environ),
			LogFn: func(f logrus.Fields, s string, p ...interface{}) { l.WithFields(f).Infof(s, p...) },
			ErrFn: func(f logrus.Fields, s string, p ...interface{}) { l.WithFields(f).Errorf(s, p...) },
		}, conf.BaseURL)
	}
	if typ == config.Postgres {
		host := c.String("host")
		if host == "" {
			host = "localhost"
		}
		port := c.Int64("port")
		if port == 0 {
			port = 5432
		}
		user := c.String("user")
		if user == "" {
			user = "fedbox"
		}
		pw, err := loadPwFromStdin(true, "%s@%s's", user, host)
		if err != nil {
			return err
		}
		fedboxDBName := "fedbox"
		oauthDBName := "oauth"
		aDb = auth.NewPgDBStore(auth.PgConfig{
			Enabled: true,
			Host:    host,
			Port:    port,
			User:    user,
			Pw:      string(pw),
			Name:    fedboxDBName,
			LogFn:   func(f logrus.Fields, s string, p ...interface{}) { l.WithFields(f).Infof(s, p...) },
			ErrFn:   func(f logrus.Fields, s string, p ...interface{}) { l.WithFields(f).Errorf(s, p...) },
		})
		db, err = pgx.New(config.BackendConfig{
			Enabled: true,
			Host:    host,
			Port:    port,
			User:    user,
			Pw:      string(pw),
			Name:    oauthDBName,
		}, conf.BaseURL, l)
		if err != nil {
			errf("Error: %s\n", err)
			//return err
		}
		return nil
	}

	*o = cmd.New(aDb, db, conf)
	return nil
}

func loadPwFromStdin(confirm bool, s string, params ...interface{}) ([]byte, error) {
	fmt.Printf(s+" pw: ", params...)
	pw1, _ := terminal.ReadPassword(0)
	fmt.Println()
	if confirm {
		fmt.Printf("pw again: ")
		pw2, _ := terminal.ReadPassword(0)
		fmt.Println()
		if !bytes.Equal(pw1, pw2) {
			return nil, errors.Errorf("Passwords do not match")
		}
	}
	return pw1, nil
}

var version = "HEAD"

func main() {
	var ctl cmd.Control

	logger := logrus.New()
	logger.Level = logrus.ErrorLevel

	app := cli.App{}
	app.Name = "fedbox-ctl"
	app.Usage = "helper utility to manage a fedbox instance"
	app.Version = version
	app.Before = func(c *cli.Context) error {
		return nil
	}
	app.Flags = []cli.Flag{
		&cli.StringFlag{
			Name:  "url",
			Usage: "The url used by the application (REQUIRED)",
		},
		&cli.StringFlag{
			Name:  "env",
			Usage: fmt.Sprintf("The environment to use. Possible values: %q, %q, %q", env.DEV, env.QA, env.PROD),
			Value: string(env.DEV),
		},
		&cli.StringFlag{
			Name:  "type",
			Usage: fmt.Sprintf("Type of the backend to use. Possible values: %q, %q", config.BoltDB, config.Postgres),
			Value: string(config.BoltDB),
		},
		&cli.StringFlag{
			Name:  "path",
			Value: ".",
			Usage: "The folder where Bolt DBs",
		},
		&cli.StringFlag{
			Name:  "host",
			Value: "localhost",
			Usage: "The postgres database host",
		},
		&cli.Int64Flag{
			Name:  "port",
			Value: 5432,
			Usage: "The postgres database port",
		},
		&cli.StringFlag{
			Name:  "user",
			Value: "fedbox",
			Usage: "The postgres database user",
		},
	}
	app.Commands = []*cli.Command{
		{
			Name:  "actor",
			Usage: "Actor management helper",
			Before: func(c *cli.Context) error {
				return setup(c, logger, &ctl)
			},
			Subcommands: []*cli.Command{
				{
					Name:    "add",
					Aliases: []string{"new"},
					Usage:   "Adds an ActivityPub actor",
					Flags: []cli.Flag{
						&cli.StringFlag{
							Name:  "type",
							Usage: fmt.Sprintf("The type of activitypub actor to add"),
						},
					},
					Action: func(c *cli.Context) error {
						names := c.Args().Slice()

						var actors = make(pub.ItemCollection, 0)
						for _, name := range names {

							pw, err := loadPwFromStdin(true, "%s's", name)
							if err != nil {
								return err
							}
							typ := pub.ActivityVocabularyType(c.String("type"))
							if !pub.ActorTypes.Contains(typ) {
								typ = pub.PersonType
							}
							p, err := ctl.AddActor(name, typ, nil, pw)
							if err != nil {
								errf("Error adding %s: %s\n", name, err)
							}
							fmt.Printf("Added %q [%s]: %s\n", typ, name, p.GetLink())
							actors = append(actors, p)
						}
						return nil
					},
				},
				{
					Name:    "del",
					Aliases: []string{"delete", "remove", "rm"},
					Usage:   "Deletes an ActivityPub actor",
					Action: func(c *cli.Context) error {
						ids := c.Args().Slice()

						for _, id := range ids {
							err := ctl.DeleteActor(id)
							if err != nil {
								errf("Error deleting %s: %s\n", id, err)
								continue
							}
							fmt.Printf("Deleted: %s\n", id)
						}
						return nil
					},
				},
				{
					Name:    "ls",
					Aliases: []string{"list"},
					Usage:   "Lists existing actors",
					Action: func(c *cli.Context) error {
						actors, err := ctl.ListActors()
						if err != nil {
							return err
						}
						for i, it := range actors {
							if act, err := pub.ToActor(it); err != nil {
								fmt.Printf("%3d [%11s] %s\n", i, it.GetType(), it.GetLink())
							} else {
								fmt.Printf("%3d [%11s] %s\n%s\n", i, it.GetType(), act.PreferredUsername.First(), it.GetLink())
							}
						}
						return nil
					},
				},
			},
		},
		{
			Name:  "oauth",
			Usage: "OAuth2 client and access token helper",
			Subcommands: []*cli.Command{
				{
					Name:  "client",
					Usage: "OAuth2 client application management",
					Before: func(c *cli.Context) error {
						return setup(c, logger, &ctl)
					},
					Subcommands: []*cli.Command{
						{
							Name:    "add",
							Aliases: []string{"new"},
							Usage:   "Adds an OAuth2 client",
							Flags: []cli.Flag{
								&cli.StringSliceFlag{
									Name:  "redirectUri",
									Value: nil,
									Usage: "The redirect URIs for current application",
								},
							},
							Action: func(c *cli.Context) error {
								redirectURIs := c.StringSlice("redirectUri")
								if len(redirectURIs) < 1 {
									return errors.Newf("Need to provide at least a return URI for the client")
								}
								pw, err := loadPwFromStdin(true, "client's")
								if err != nil {
									errf(err.Error())
								}
								id, err := ctl.AddClient(pw, redirectURIs, nil)
								if err == nil {
									fmt.Printf("Client ID: %s\n", id)
								}
								return err
							},
						},
						{
							Name:      "del",
							Aliases:   []string{"delete", "remove", "rm"},
							Usage:     "Removes an existing OAuth2 client",
							ArgsUsage: "APPLICATION_UUID...",
							Action: func(c *cli.Context) error {
								for i := 0; i <= c.Args().Len(); i++ {
									id := c.Args().Get(i)
									err := ctl.DeleteClient(id)
									if err != nil {
										errf("Error deleting %s: %s\n", id, err)
										continue
									}
									fmt.Printf("Deleted: %s\n", id)
								}
								return nil
							},
						},
						{
							Name:    "ls",
							Aliases: []string{"list"},
							Usage:   "Lists existing OAuth2 clients",
							Action: func(c *cli.Context) error {
								clients, err := ctl.ListClients()
								if err != nil {
									return err
								}
								for i, client := range clients {
									fmt.Printf("%d %s - %s\n", i, client.GetId(), strings.ReplaceAll(client.GetRedirectUri(), "\n", " :: "))
								}
								return nil
							},
						},
					},
				},
				{
					Name:  "token",
					Usage: "OAuth2 authorization token management",
					Before: func(c *cli.Context) error {
						return setup(c, logger, &ctl)
					},
					Subcommands: []*cli.Command{
						{
							Name:    "add",
							Aliases: []string{"new", "get"},
							Usage:   "Adds an OAuth2 token",
							Flags: []cli.Flag{
								&cli.StringFlag{
									Name:  "client",
									Usage: "The client to use for generating the token",
								},
								&cli.StringFlag{
									Name:  "actor",
									Usage: "The actor identifier we want to generate the authorization for (ID)",
								},
							},
							Action: func(c *cli.Context) error {
								clientID := c.String("client")
								if clientID == "" {
									return errors.Newf("Need to provide the client id")
								}
								actor := c.String("actor")
								if clientID == "" {
									return errors.Newf("Need to provide the actor identifier (ID)")
								}
								tok, err := ctl.GenAuthToken(clientID, actor, nil)
								if err == nil {
									fmt.Printf("Authorization: Bearer %s\n", tok)
								}
								return err
							},
						},
					},
				},
			},
		},
		{
			Name:  "bootstrap",
			Usage: "Bootstrap a new postgres or bolt database helper",
			Before: func(c *cli.Context) error {
				return setup(c, logger, &ctl)
			},
			Flags: []cli.Flag{
				&cli.StringFlag{
					Name:  "root",
					Usage: "root account of postgres server (default: postgres)",
					Value: "postgres",
				},
				&cli.StringFlag{
					Name:  "sql",
					Usage: "path to the queries for initializing the database",
					Value: "postgres",
				},
			},
			Action: func(c *cli.Context) error {
				dir := c.String("dir")
				if dir == "" {
					dir = "."
				}
				environ := env.Type(c.String("env"))
				if environ == "" {
					environ = env.DEV
				}
				typ := config.StorageType(c.String("type"))
				if typ == "" {
					typ = config.BoltDB
				}
				return ctl.Bootstrap(dir, typ, environ)
			},
			Subcommands: []*cli.Command{
				{
					Name:  "reset",
					Usage: "reset an existing database",
					Action: func(c *cli.Context) error {
						dir := c.String("dir")
						if dir == "" {
							dir = "."
						}
						environ := env.Type(c.String("env"))
						if environ == "" {
							environ = env.DEV
						}
						typ := config.StorageType(c.String("type"))
						if typ == "" {
							typ = config.BoltDB
						}
						err := ctl.BootstrapReset(dir, typ, environ)
						if err != nil {
							return err
						}
						return ctl.Bootstrap(dir, typ, environ)
					},
				},
			},
		},
	}

	err := app.Run(os.Args)
	if err != nil {
		errf("Error: %s\n", err)
		os.Exit(1)
	}
}
