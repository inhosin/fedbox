package boltdb

import (
	pub "github.com/go-ap/activitypub"
	"github.com/go-ap/errors"
	"github.com/go-ap/fedbox/activitypub"
	"github.com/go-ap/handlers"
	"github.com/go-ap/jsonld"
	bolt "go.etcd.io/bbolt"
)

func Bootstrap(path string, baseURL string) error {
	var err error
	db, err := bolt.Open(path, 0600, nil)
	if err != nil {
		return errors.Annotatef(err, "could not open db")
	}
	defer db.Close()

	return createService(db, activitypub.Self(activitypub.DefaultServiceIRI(baseURL)))
}

func Clean(path string) error {
	var err error
	db, err := bolt.Open(path, 0600, nil)
	if err != nil {
		return errors.Annotatef(err, "could not open db")
	}
	defer db.Close()

	err = db.Update(func(tx *bolt.Tx) error {
		return tx.DeleteBucket([]byte(rootBucket))
	})
	return err
}

// FIXME(marius): I feel like this hasn't been used anywhere and as such might not work
func AddTestMockActor(path string, actor pub.Actor) error {
	var err error
	db, err := bolt.Open(path, 0600, nil)
	if err != nil {
		return errors.Annotatef(err, "could not open db")
	}
	defer db.Close()

	itPath, err := itemBucketPath(actor.GetLink())
	if err != nil {
		return errors.Annotatef(err, "invalid actor ID %s", actor.GetLink())
	}

	err = db.Update(func(tx *bolt.Tx) error {
		root := tx.Bucket([]byte(rootBucket))

		raw, _ := jsonld.Marshal(actor)
		actorBucket, _, err := descendInBucket(root, itPath, true)
		actorBucket.Put([]byte(handlers.Inbox), nil)
		actorBucket.Put([]byte(handlers.Outbox), nil)
		actorBucket.Put([]byte(handlers.Following), nil)
		actorBucket.Put([]byte(handlers.Followers), nil)
		actorBucket.Put([]byte(handlers.Liked), nil)
		actorBucket.Put([]byte(handlers.Likes), nil)
		actorBucket.Put([]byte(handlers.Shares), nil)
		if err != nil {
			return errors.Errorf("could not create actor bucket: %s", err)
		}
		err = actorBucket.Put([]byte(objectKey), raw)
		if err != nil {
			return errors.Errorf("could not insert entry: %s", err)
		}

		//actors := hostBucket.Bucket([]byte(bucketActors))
		//if actors == nil {
		//	return errors.Annotatef(err, "could not open %s bucket", bucketActors)
		//}
		//if !actors.Writable() {
		//	return errors.Errorf("Non writeable bucket %s", bucketActors)
		//}
		return nil
	})

	return err
}
