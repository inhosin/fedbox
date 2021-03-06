package boltdb

import (
	"bytes"
	"fmt"
	pub "github.com/go-ap/activitypub"
	"github.com/go-ap/errors"
	ap "github.com/go-ap/fedbox/activitypub"
	"github.com/go-ap/handlers"
	"github.com/go-ap/jsonld"
	s "github.com/go-ap/storage"
	"github.com/mariusor/qstring"
	"github.com/sirupsen/logrus"
	bolt "go.etcd.io/bbolt"
	"golang.org/x/crypto/bcrypt"
	"path"
	"sort"
	"time"
)

type repo struct {
	d       *bolt.DB
	baseURL string
	root    []byte
	path    string
	logFn   loggerFn
	errFn   loggerFn
}

type loggerFn func(logrus.Fields, string, ...interface{})

const (
	rootBucket       = ":"
	bucketActors     = "actors"
	bucketActivities = "activities"
	bucketObjects    = "objects"
)

// Config
type Config struct {
	Path       string
	BucketName string
	LogFn      loggerFn
	ErrFn      loggerFn
}

// New returns a new repo repository
func New(c Config, baseURL string) *repo {
	b := repo{
		root:    []byte(rootBucket),
		path:    c.Path,
		baseURL: baseURL,
		logFn:   func(logrus.Fields, string, ...interface{}) {},
		errFn:   func(logrus.Fields, string, ...interface{}) {},
	}
	if c.ErrFn != nil {
		b.errFn = c.ErrFn
	}
	if c.LogFn != nil {
		b.logFn = c.LogFn
	}
	return &b
}

func loadItem(raw []byte) (pub.Item, error) {
	if raw == nil || len(raw) == 0 {
		// TODO(marius): log this instead of stopping the iteration and returning an error
		return nil, errors.Errorf("empty raw item")
	}
	return pub.UnmarshalJSON(raw)
}

func filterIt(it pub.Item, f s.Filterable) (pub.Item, error) {
	if it == nil {
		return it, nil
	}
	if ff, ok := f.(ap.ItemMatcher); ok {
		if ff.ItemMatches(it) {
			return it, nil
		} else {
			return nil, nil
		}
	}
	if f1, ok := f.(s.Filterable); ok {
		if f1.GetLink() == it.GetLink() {
			return it, nil
		}
	}
	if f1, ok := f.(s.FilterableItems); ok {
		iris := f1.IRIs()
		// FIXME(marius): the Contains method returns true for the case where IRIs is empty, we don't want that
		if len(iris) > 0 && !iris.Contains(it.GetLink()) {
			return nil, nil
		}
		types := f1.Types()
		// FIXME(marius): this does not cover case insensitivity
		if len(types) > 0 && !types.Contains(it.GetType()) {
			return nil, nil
		}
		return it, nil
	}
	return nil, errors.Errorf("Invalid filter %T", f)
}

func loadOneFromBucket(db *bolt.DB, root []byte, f s.Filterable) (pub.Item, error) {
	col, cnt, err := loadFromBucket(db, root, f)
	if err != nil || cnt == 0 {
		return nil, err
	}
	return col.First(), nil
}

func createService(b *bolt.DB, service pub.Service) error {
	raw, err := jsonld.Marshal(service)
	if err != nil {
		return errors.Annotatef(err, "could not marshal service json")
	}
	err = b.Update(func(tx *bolt.Tx) error {
		root, err := tx.CreateBucketIfNotExists([]byte(rootBucket))
		if err != nil {
			return errors.Annotatef(err, "could not create root bucket")
		}
		path, err := itemBucketPath(service.GetLink())
		if err != nil {
			return err
		}
		hostBucket, _, err := descendInBucket(root, path, true)
		if err != nil {
			return errors.Annotatef(err, "could not create %s bucket", path)
		}
		err = hostBucket.Put([]byte(objectKey), raw)
		if err != nil {
			return errors.Annotatef(err, "could not save %s[%s]", service.Name, service.Type)
		}
		_, err = hostBucket.CreateBucketIfNotExists([]byte(bucketActivities))
		if err != nil {
			return errors.Annotatef(err, "could not create %s bucket", bucketActivities)
		}
		_, err = hostBucket.CreateBucketIfNotExists([]byte(bucketActors))
		if err != nil {
			return errors.Annotatef(err, "could not create %s bucket", bucketActors)
		}
		_, err = hostBucket.CreateBucketIfNotExists([]byte(bucketObjects))
		if err != nil {
			return errors.Annotatef(err, "could not create %s bucket", bucketObjects)
		}
		return nil
	})
	if err != nil {
		return errors.Annotatef(err, "could not create buckets")
	}

	return nil
}

func (r *repo) CreateService (service pub.Service) error {
	var err error
	if err = r.Open(); err != nil {
		return err
	}
	defer r.Close()
	return createService(r.d, service)
}

func loadFromBucket(db *bolt.DB, root []byte, f s.Filterable) (pub.ItemCollection, uint, error) {
	col := make(pub.ItemCollection, 0)

	err := db.View(func(tx *bolt.Tx) error {
		rb := tx.Bucket(root)
		if rb == nil {
			return errors.Errorf("Invalid bucket %s", root)
		}

		var remainderPath []byte
		iri := f.GetLink()
		if iri != "" {
			var err error
			// This is the case where the Filter points to a single AP Object IRI
			// TODO(marius): Ideally this should support the case where we use the IRI to point to a bucket path
			//     and on top of that apply the other filters
			remainderPath, err = itemBucketPath(iri)
			if err != nil {
				return err
			}
		}
		create := false
		var err error
		var b *bolt.Bucket
		// Assume bucket exists and has keys
		b, remainderPath, err = descendInBucket(rb, remainderPath, create)
		if err != nil {
			return err
		}
		if b == nil {
			return errors.Errorf("Invalid bucket %s/%s", root, remainderPath)
		}

		c := b.Cursor()
		if c == nil {
			return errors.Errorf("Invalid bucket cursor %s/%s", root, remainderPath)
		}
		isObjectKey := func(k []byte) bool {
			return string(k) == objectKey || string(k) == metaDataKey
		}
		// if no path was returned from descendIntoBucket we iterate over all keys in the current bucket
		for key, raw := c.First(); key != nil; key, raw = c.Next() {
			if !isObjectKey(key) {
				// FIXME(marius): I guess this should not happen (pub descendIntoBucket should 'descend' into 'path'
				//    if it's a valid bucket)
				b := b.Bucket(key)
				if b == nil {
					continue
				}
				key = []byte(objectKey)
				raw = b.Get(key)
			}
			if handlers.ValidCollection(path.Base(f.GetLink().String())) {
				colIRIs := make(pub.IRIs, 0)
				err = jsonld.Unmarshal(raw, &colIRIs)
				for _, iri := range colIRIs {
					it, _ := loadOneFromBucket(db, root, ap.Filters{IRI: iri})
					if it != nil {
						col = append(col, it)
					}
				}
			} else {
				it, err := loadItem(raw)
				if err != nil {
					continue
				}
				if err != nil {
					continue
				}
				if it != nil {
					col = append(col, it)
				}
			}
		}
		return nil
	})
	for _, it := range col {
		// Remove bcc and bto
		if s, ok := it.(pub.HasRecipients); ok {
			s.Clean()
		}
	}

	return col, uint(len(col)), err
}

func itemsLess(i1, i2 pub.Item) bool {
	o1, e1 := pub.ToObject(i1)
	o2, e2 := pub.ToObject(i2)
	if e1 != nil || e2 != nil {
		return false
	}
	if !o1.Updated.IsZero() || !o2.Updated.IsZero() {
		return o1.Updated.Sub(o2.Updated) > 0
	}
	if !o1.Published.IsZero() || o2.Published.IsZero() {
		return o1.Published.Sub(o2.Published) > 0
	}
	return false
}

func orderItems(col pub.ItemCollection) pub.ItemCollection {
	sort.SliceStable(col, func(i, j int) bool {
		return itemsLess(col[i], col[j])
	})
	return col
}

func (r repo) buildIRIs(c handlers.CollectionType, hashes ...ap.Hash) pub.IRIs {
	iris := make(pub.IRIs, 0)
	for _, hash := range hashes {
		i := pub.IRI(fmt.Sprintf("%s/%s/%s", r.baseURL, c, hash))
		iris = append(iris, i)
	}
	return iris
}

// Load
func (r *repo) Load(f s.Filterable) (pub.ItemCollection, uint, error) {
	var err error
	if r.Open(); err != nil {
		return nil, 0, err
	}
	defer r.Close()

	items := make(pub.ItemCollection, 0)
	unfiltered, _, err := loadFromBucket(r.d, r.root, f)
	for _, it := range unfiltered {
		it, _ = filterIt(it, f)
		if it == nil {
			continue
		}
		items = append(items, it)
	}
	return orderItems(items), uint(len(items)), err
}

// LoadActivities
func (r *repo) LoadActivities(f s.Filterable) (pub.ItemCollection, uint, error) {
	return r.Load(f)
}

// LoadObjects
func (r *repo) LoadObjects(f s.Filterable) (pub.ItemCollection, uint, error) {
	return r.Load(f)
}

// LoadActors
func (r *repo) LoadActors(f s.Filterable) (pub.ItemCollection, uint, error) {
	return r.Load(f)
}

func descendInBucket(root *bolt.Bucket, path []byte, create bool) (*bolt.Bucket, []byte, error) {
	if root == nil {
		return nil, path, errors.Newf("trying to descend into nil bucket")
	}
	if len(path) == 0 {
		return root, path, nil
	}
	buckets := bytes.Split(path, []byte{'/'})

	lvl := 0
	b := root
	// descend the bucket tree up to the last found bucket
	for _, name := range buckets {
		lvl++
		if len(name) == 0 {
			continue
		}
		if b == nil {
			return root, path, errors.Errorf("trying to load from nil bucket")
		}
		var cb *bolt.Bucket
		if create {
			cb, _ = b.CreateBucketIfNotExists(name)
		} else {
			cb = b.Bucket(name)
		}
		if cb == nil {
			lvl--
			break
		}
		b = cb
	}
	remBuckets := buckets[lvl:]
	path = bytes.Join(remBuckets, []byte{'/'})
	if len(remBuckets) > 0 {
		return b, path, errors.NotFoundf("%s not found", remBuckets[0])
	}
	return b, path, nil
}

// LoadCollection
func (r *repo) LoadCollection(f s.Filterable) (pub.CollectionInterface, error) {
	var err error
	err = r.Open()
	if err != nil {
		return nil, err
	}
	defer r.Close()

	var ret pub.CollectionInterface
	iri := f.GetLink()
	url, err := iri.URL()
	if err != nil {
		r.errFn(nil, "invalid IRI filter element %s when loading collections", iri)
	}

	qstr, _ := qstring.Marshal(f)
	url.RawQuery = qstr.Encode()

	col := &pub.OrderedCollection{}
	col.ID = pub.ID(url.String())
	col.Type = pub.OrderedCollectionType

	elements, count, err := loadFromBucket(r.d, r.root, f)
	if err != nil {
		return nil, err
	}
	if count == 0 {
		return col, nil
	}
	for _, it := range orderItems(elements) {
		it, _ = filterIt(it, f)
		if it == nil {
			continue
		}
		if err = col.Append(it); err == nil {
			col.TotalItems++
		}
	}

	ret = col
	return ret, err
}

const objectKey = "__raw"
const metaDataKey = "__meta_data"

func delete(r *repo, it pub.Item) (pub.Item, error) {
	f := ap.Filters{
		IRI: it.GetLink(),
	}
	if it.IsObject() {
		f.Type = []pub.ActivityVocabularyType{it.GetType()}
	}
	old, _ := loadOneFromBucket(r.d, r.root, &f)

	// TODO(marius): add some mechanism for marking the collections pub read-only
	//    update 2019-10-03: I have no clue what this comment means. I can't think of why we'd need r/o collections for
	//    cases where we want to delete things.
	t := pub.Tombstone{
		ID:   pub.ID(it.GetLink()),
		Type: pub.TombstoneType,
		To: pub.ItemCollection{
			pub.PublicNS,
		},
		Deleted:    time.Now().UTC(),
		FormerType: old.GetType(),
	}
	return save(r, t)
}

func (r *repo) CreateCollection(col pub.CollectionInterface) (pub.CollectionInterface, error) {
	var err error
	err = r.Open()
	if err != nil {
		return col, err
	}
	defer r.Close()

	cPath, err := itemBucketPath(col.GetLink())
	if err != nil {
		return col, errors.Annotatef(err, "Unable to load a valid IRI from object")
	}
	c := []byte(path.Base(string(cPath)))
	err = r.d.Update(func(tx *bolt.Tx) error {
		root := tx.Bucket(r.root)
		b, _, err := descendInBucket(root, cPath, true)
		if err != nil {
			return errors.Annotatef(err, "Unable to find path %s/%s", r.root, cPath)
		}
		return b.Put(c, nil)
	})
	return col, err
}

func itemBucketPath(iri pub.IRI) ([]byte, error) {
	url, err := iri.URL()
	if err != nil {
		return nil, errors.Annotatef(err, "invalid IRI")
	}
	return []byte(url.Host + url.Path), nil
}

func save(r *repo, it pub.Item) (pub.Item, error) {
	path, err := itemBucketPath(it.GetLink())
	if err != nil {
		return it, errors.Annotatef(err, "Unable to load a valid IRI from object")
	}
	var uuid []byte
	err = r.d.Update(func(tx *bolt.Tx) error {
		root := tx.Bucket(r.root)
		if root == nil {
			return errors.Errorf("Invalid bucket %s", r.root)
		}
		if !root.Writable() {
			return errors.Errorf("Non writeable bucket %s", r.root)
		}
		var b *bolt.Bucket
		b, uuid, err = descendInBucket(root, path, true)
		if err != nil {
			return errors.Annotatef(err, "Unable to find %s in root bucket", path)
		}
		if !b.Writable() {
			return errors.Errorf("Non writeable bucket %s", path)
		}
		if len(uuid) > 0 {
			b, err = b.CreateBucket(uuid)
			if err != nil {
				return errors.Annotatef(err, "could not create bucket %s", uuid)
			}
		}
		return nil
	})

	err = r.d.Update(func(tx *bolt.Tx) error {
		root := tx.Bucket(r.root)
		if root == nil {
			return errors.Errorf("Invalid bucket %s", r.root)
		}
		if !root.Writable() {
			return errors.Errorf("Non writeable bucket %s", r.root)
		}
		var b *bolt.Bucket
		b, uuid, err = descendInBucket(root, path, true)
		if err != nil {
			return errors.Annotatef(err, "Unable to find %s in root bucket", path)
		}
		if !b.Writable() {
			return errors.Errorf("Non writeable bucket %s", path)
		}
		// TODO(marius): it's possible to set the encoding/decoding functions on the package or storage object level
		//  instead of using jsonld.(Un)Marshal like this.
		entryBytes, err := jsonld.Marshal(it)
		if err != nil {
			return errors.Annotatef(err, "could not marshal object")
		}
		err = b.Put([]byte(objectKey), entryBytes)
		if err != nil {
			return errors.Annotatef(err, "could not store encoded object")
		}

		return nil
	})

	return it, err
}

// SaveActivity
func (r *repo) SaveActivity(it pub.Item) (pub.Item, error) {
	return r.SaveObject(it)
}

func (r *repo) SaveActor(it pub.Item) (pub.Item, error) {
	return r.SaveObject(it)
}

// SaveObject
func (r *repo) SaveObject(it pub.Item) (pub.Item, error) {
	var err error
	err = r.Open()
	if err != nil {
		return it, err
	}
	defer r.Close()

	if it, err = save(r, it); err == nil {
		op := "Updated"
		id := it.GetID()
		if !id.IsValid() {
			op = "Added new"
		}
		r.logFn(nil, "%s %s: %s", op, it.GetType(), it.GetLink())
	}

	return it, err
}

// IsLocalIRI shows if the received IRI belongs to the current instance
func (r repo) IsLocalIRI(i pub.IRI) bool {
	return i.Contains(pub.IRI(r.baseURL), false)
}

func (r *repo) RemoveFromCollection(col pub.IRI, it pub.Item) error {
	if it == nil {
		return errors.Newf("Unable to add nil element to collection")
	}
	if len(col) == 0 {
		return errors.Newf("Unable to find collection")
	}
	if len(it.GetLink()) == 0 {
		return errors.Newf("Invalid create collection does not have a valid IRI")
	}
	if !r.IsLocalIRI(col.GetLink()) {
		return errors.Newf("Unable to save to non local collection %s", col)
	}
	path, err := itemBucketPath(col.GetLink())
	if err != nil {
		return err
	}

	err = r.Open()
	if err != nil {
		return err
	}
	defer r.Close()

	return r.d.Update(func(tx *bolt.Tx) error {
		var rem []byte
		root := tx.Bucket(r.root)
		if root == nil {
			return errors.Errorf("Invalid bucket %s", r.root)
		}
		if !root.Writable() {
			return errors.Errorf("Non writeable bucket %s", r.root)
		}
		var b *bolt.Bucket
		b, rem, err = descendInBucket(root, path, true)
		if err != nil {
			return errors.Newf("Unable to find %s in root bucket", path)
		}
		if len(rem) == 0 {
			rem = []byte(objectKey)
		}
		if !b.Writable() {
			return errors.Errorf("Non writeable bucket %s", path)
		}
		var iris pub.IRIs
		raw := b.Get(rem)
		if len(raw) > 0 {
			err := jsonld.Unmarshal(raw, &iris)
			if err != nil {
				return errors.Newf("Unable to unmarshal entries in collection %s", path)
			}
		}
		for k, iri := range iris {
			if iri == it.GetLink() {
				iris = append(iris[:k], iris[k+1:]...)
				break
			}
		}
		raw, err := jsonld.Marshal(iris)
		if err != nil {
			return errors.Newf("Unable to marshal entries in collection %s", path)
		}
		err = b.Put(rem, raw)
		if err != nil {
			return errors.Newf("Unable to save entries to collection %s", path)
		}

		return err
	})
}

func (r *repo) AddToCollection(col pub.IRI, it pub.Item) error {
	if it == nil {
		return errors.Newf("Unable to add nil element to collection")
	}
	if len(col) == 0 {
		return errors.Newf("Unable to find collection")
	}
	if len(it.GetLink()) == 0 {
		return errors.Newf("Invalid create collection does not have a valid IRI")
	}
	if !r.IsLocalIRI(col.GetLink()) {
		return errors.Newf("Unable to save to non local collection %s", col)
	}
	path, err := itemBucketPath(col.GetLink())
	if err != nil {
		return err
	}

	err = r.Open()
	if err != nil {
		return err
	}
	defer r.Close()

	return r.d.Update(func(tx *bolt.Tx) error {
		var rem []byte
		root := tx.Bucket(r.root)
		if root == nil {
			return errors.Errorf("Invalid bucket %s", r.root)
		}
		if !root.Writable() {
			return errors.Errorf("Non writeable bucket %s", r.root)
		}
		var b *bolt.Bucket
		b, rem, err = descendInBucket(root, path, true)
		if err != nil {
			return errors.Newf("Unable to find %s in root bucket", path)
		}
		if len(rem) == 0 {
			rem = []byte(objectKey)
		}
		if !b.Writable() {
			return errors.Errorf("Non writeable bucket %s", path)
		}
		var iris pub.IRIs
		raw := b.Get(rem)
		if len(raw) > 0 {
			err := jsonld.Unmarshal(raw, &iris)
			if err != nil {
				return errors.Newf("Unable to unmarshal entries in collection %s", path)
			}
		}
		if iris.Contains(it.GetLink()) {
			return errors.Newf("Element already exists in collection %s", path)
		}
		iris = append(iris, it.GetLink())
		raw, err := jsonld.Marshal(iris)
		if err != nil {
			return errors.Newf("Unable to marshal entries in collection %s", path)
		}
		err = b.Put(rem, raw)
		if err != nil {
			return errors.Newf("Unable to save entries to collection %s", path)
		}
		return err
	})
}

func (r *repo) UpdateActor(it pub.Item) (pub.Item, error) {
	return r.UpdateObject(it)
}

// UpdateObject
func (r *repo) UpdateObject(it pub.Item) (pub.Item, error) {
	return r.SaveObject(it)
}

func (r *repo) DeleteActor(it pub.Item) (pub.Item, error) {
	return r.DeleteObject(it)
}

// DeleteObject
func (r *repo) DeleteObject(it pub.Item) (pub.Item, error) {
	var err error
	err = r.Open()
	if err != nil {
		return it, err
	}
	defer r.Close()
	var bucket string
	if pub.ActivityTypes.Contains(it.GetType()) {
		bucket = bucketActivities
	} else if pub.ActorTypes.Contains(it.GetType()) {
		bucket = bucketActors
	} else {
		bucket = bucketObjects
	}
	if it, err = delete(r, it); err == nil {
		r.logFn(nil, "Added new %s: %s", bucket[:len(bucket)-1], it.GetLink())
	}
	return it, err
}

// GenerateID
func (r *repo) GenerateID(it pub.Item, by pub.Item) (pub.ID, error) {
	typ := it.GetType()

	var partOf string
	if pub.ActivityTypes.Contains(typ) {
		partOf = fmt.Sprintf("%s/%s", r.baseURL, ap.ActivitiesType)
	} else if pub.ActorTypes.Contains(typ) || typ == pub.ActorType {
		partOf = fmt.Sprintf("%s/%s", r.baseURL, ap.ActorsType)
	} else if pub.ObjectTypes.Contains(typ) {
		partOf = fmt.Sprintf("%s/%s", r.baseURL, ap.ObjectsType)
	}
	return ap.GenerateID(it, partOf, by)
}

func (r *repo) Open() error {
	var err error
	r.d, err = bolt.Open(r.path, 0600, nil)
	if err != nil {
		return errors.Annotatef(err, "Could not open db %s", r.path)
	}
	return nil
}

// Close closes the boltdb database if possible.
func (r *repo) Close() error {
	if r.d == nil {
		return nil
	}
	return r.d.Close()
}

type meta struct {
	Pw []byte `json:"pw"`
}

// PasswordSet
func (r *repo) PasswordSet(it pub.Item, pw []byte) error {
	path, err := itemBucketPath(it.GetLink())
	if err != nil {
		return err
	}
	err = r.Open()
	if err != nil {
		return err
	}
	defer r.Close()

	err = r.d.Update(func(tx *bolt.Tx) error {
		root := tx.Bucket(r.root)
		if root == nil {
			return errors.Errorf("Invalid bucket %s", r.root)
		}
		if !root.Writable() {
			return errors.Errorf("Non writeable bucket %s", r.root)
		}
		var b *bolt.Bucket
		b, _, err = descendInBucket(root, path, true)
		if err != nil {
			return errors.Newf("Unable to find %s in root bucket", path)
		}
		if !b.Writable() {
			return errors.Errorf("Non writeable bucket %s", path)
		}

		pw, err = bcrypt.GenerateFromPassword(pw, -1)
		if err != nil {
			return errors.Annotatef(err, "Could not encrypt the pw")
		}
		m := meta{
			Pw: pw,
		}
		entryBytes, err := jsonld.Marshal(m)
		if err != nil {
			return errors.Annotatef(err, "Could not marshal metadata")
		}
		err = b.Put([]byte(metaDataKey), entryBytes)
		if err != nil {
			return errors.Errorf("Could not insert entry: %s", err)
		}
		return nil
	})

	return err
}

// PasswordCheck
func (r *repo) PasswordCheck(it pub.Item, pw []byte) error {
	path, err := itemBucketPath(it.GetLink())
	if err != nil {
		return err
	}
	err = r.Open()
	if err != nil {
		return err
	}
	defer r.Close()

	m := meta{}
	err = r.d.View(func(tx *bolt.Tx) error {
		root := tx.Bucket(r.root)
		if root == nil {
			return errors.Errorf("Invalid bucket %s", r.root)
		}
		var b *bolt.Bucket
		b, path, err = descendInBucket(root, path, false)
		if err != nil {
			return errors.Newf("Unable to find %s in root bucket", path)
		}
		entryBytes := b.Get([]byte(metaDataKey))
		err := jsonld.Unmarshal(entryBytes, &m)
		if err != nil {
			return errors.Annotatef(err, "Could not unmarshal metadata")
		}
		if err := bcrypt.CompareHashAndPassword(m.Pw, pw); err != nil {
			return errors.NewUnauthorized(err, "Invalid pw")
		}
		return nil
	})
	return err
}
